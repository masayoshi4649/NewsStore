// Package ingest は、外部取得と PostgreSQL 保存を一つの実行単位として管理します。
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/connector"
	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

const finalizationTimeout = 5 * time.Second

// ErrAlreadyRunning は、同じ取得対象が既に実行中であることを表します。
var ErrAlreadyRunning = errors.New("同じ取得対象を既に実行中です")

// Repository は、記事取得サービスが必要とする永続化操作を表します。
type Repository interface {
	GetSource(ctx context.Context, id int64) (domain.Source, error)
	StartRun(ctx context.Context, sourceID int64, triggerType string) (int64, error)
	FinishRunAndSource(
		ctx context.Context,
		runID int64,
		sourceID int64,
		result postgres.RunResult,
	) error
	InsertArticle(ctx context.Context, article domain.Article) (bool, error)
	AcquireSourceLock(
		ctx context.Context,
		sourceID int64,
	) (release func(context.Context) error, acquired bool, err error)
}

// Summary は、一回の記事取得で確定した件数と警告を表します。
type Summary struct {
	RunID         int64
	Status        postgres.RunStatus
	FetchedCount  int
	InsertedCount int
	SkippedCount  int
	WarningCount  int
	Warnings      []string
}

// Service は、コネクタ選択、記事検証、冪等保存、履歴更新を管理します。
type Service struct {
	repository Repository
	registry   *connector.Registry
	logger     *slog.Logger
	activeMu   sync.Mutex
	active     map[int64]struct{}
}

/*
New は、記事取得サービスを生成します。

機能:
  - 永続化先、コネクタ一覧、ロガーを一つのサービスへ組み立てる
  - 取得対象単位の実行中管理領域を初期化する

引数:
  - repository Repository: 取得対象、記事、履歴を保存する Repository
  - registry *connector.Registry: 取得対象種別から実装を選ぶコネクタ一覧
  - logger *slog.Logger: 実行状態とエラーを記録するロガー

返り値:
  - *Service: 初期化済みの記事取得サービス
*/
func New(repository Repository, registry *connector.Registry, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repository: repository,
		registry:   registry,
		logger:     logger,
		active:     make(map[int64]struct{}),
	}
}

// ----------------------------------------

/*
Run は、指定取得対象の記事を取得してリビジョン単位で保存します。

機能:
  - 同じ取得対象のプロセス内同時実行を抑止する
  - 実行履歴を開始し、対応するコネクタを呼び出す
  - 記事を検証して一件ずつ冪等保存する
  - 個別失敗を警告として継続し、実行履歴と次回予定を更新する

引数:
  - ctx context.Context: 取得、保存、キャンセルを伝えるコンテキスト
  - sourceID int64: 実行する取得対象 ID
  - triggerType string: scheduled、manual、cli のいずれか

返り値:
  - Summary: 実行 ID、最終状態、各件数、警告
  - error: 取得対象読込、一覧取得、または履歴更新に失敗した場合のエラー
*/
func (s *Service) Run(ctx context.Context, sourceID int64, triggerType string) (Summary, error) {
	if !s.begin(sourceID) {
		return Summary{}, ErrAlreadyRunning
	}
	defer s.end(sourceID)

	releaseLock, acquired, err := s.repository.AcquireSourceLock(ctx, sourceID)
	if err != nil {
		return Summary{}, fmt.Errorf("取得対象の排他ロックを取得できません: %w", err)
	}
	if !acquired {
		return Summary{}, ErrAlreadyRunning
	}
	if releaseLock == nil {
		return Summary{}, errors.New("取得対象の排他ロック解放関数がありません")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), finalizationTimeout)
		defer cancel()
		if err := releaseLock(releaseCtx); err != nil {
			s.logger.Error("取得対象の排他ロックを解放できません", "source_id", sourceID, "error", err)
		}
	}()

	source, err := s.repository.GetSource(ctx, sourceID)
	if err != nil {
		return Summary{}, fmt.Errorf("取得対象を読み込めません: %w", err)
	}

	runID, err := s.repository.StartRun(ctx, sourceID, triggerType)
	if err != nil {
		return Summary{}, err
	}

	fetcher, err := s.registry.Get(source.Kind)
	if err != nil {
		return s.failRun(runID, sourceID, err)
	}

	result, err := fetcher.Fetch(ctx, source)
	if err != nil {
		return s.failRun(runID, sourceID, err)
	}

	summary := Summary{
		RunID:        runID,
		Status:       postgres.RunStatusSucceeded,
		FetchedCount: len(result.Articles) + result.Skipped,
		SkippedCount: result.Skipped,
		Warnings:     append([]string(nil), result.Warnings...),
	}
	for index := range result.Articles {
		article := normalizeArticle(result.Articles[index], sourceID)
		if err := validateArticle(article); err != nil {
			summary.Warnings = append(
				summary.Warnings,
				fmt.Sprintf("記事 %d を保存しませんでした: %v", index+1, err),
			)
			continue
		}

		inserted, err := s.repository.InsertArticle(ctx, article)
		if err != nil {
			summary.Warnings = append(
				summary.Warnings,
				fmt.Sprintf("記事 %q の保存に失敗しました: %v", article.ExternalID, err),
			)
			continue
		}
		if inserted {
			summary.InsertedCount++
		} else {
			summary.SkippedCount++
		}
	}

	summary.WarningCount = len(summary.Warnings)
	if summary.WarningCount > 0 {
		summary.Status = postgres.RunStatusPartial
	}
	message := summarizeWarnings(summary.Warnings)

	finalizeCtx, cancel := context.WithTimeout(context.Background(), finalizationTimeout)
	defer cancel()
	if err := s.repository.FinishRunAndSource(finalizeCtx, runID, sourceID, postgres.RunResult{
		Status:        summary.Status,
		FetchedCount:  summary.FetchedCount,
		InsertedCount: summary.InsertedCount,
		SkippedCount:  summary.SkippedCount,
		WarningCount:  summary.WarningCount,
		ErrorMessage:  message,
	}); err != nil {
		return summary, err
	}
	s.logger.Info(
		"記事取得が完了しました",
		"source_id", sourceID,
		"run_id", runID,
		"status", summary.Status,
		"fetched", summary.FetchedCount,
		"inserted", summary.InsertedCount,
		"skipped", summary.SkippedCount,
		"warnings", summary.WarningCount,
	)
	return summary, nil
}

// ----------------------------------------

/*
failRun は、取得全体の失敗を履歴と取得対象へ記録します。

機能:
  - 失敗状態の実行履歴を完了する
  - 取得対象の最終エラーと次回予定を更新する

引数:
  - runID int64: 失敗した実行 ID
  - sourceID int64: 失敗した取得対象 ID
  - cause error: 記録する原因エラー

返り値:
  - Summary: failed 状態と実行 ID
  - error: 元の原因と履歴更新エラーを結合したエラー
*/
func (s *Service) failRun(runID, sourceID int64, cause error) (Summary, error) {
	message := truncateMessage(cause.Error(), 8000)
	summary := Summary{
		RunID:        runID,
		Status:       postgres.RunStatusFailed,
		WarningCount: 1,
		Warnings:     []string{message},
	}

	finalizeCtx, cancel := context.WithTimeout(context.Background(), finalizationTimeout)
	defer cancel()
	finishErr := s.repository.FinishRunAndSource(finalizeCtx, runID, sourceID, postgres.RunResult{
		Status:       postgres.RunStatusFailed,
		WarningCount: 1,
		ErrorMessage: message,
	})

	s.logger.Error(
		"記事取得に失敗しました",
		"source_id", sourceID,
		"run_id", runID,
		"error", cause,
	)
	return summary, errors.Join(cause, finishErr)
}

// ----------------------------------------

/*
begin は、取得対象を実行中として登録します。

機能:
  - ミューテックスで実行中マップを保護する
  - 既に同じ ID がある場合は登録しない

引数:
  - sourceID int64: 実行開始する取得対象 ID

返り値:
  - bool: 新たに登録できた場合は true、既に実行中の場合は false
*/
func (s *Service) begin(sourceID int64) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	if _, exists := s.active[sourceID]; exists {
		return false
	}
	s.active[sourceID] = struct{}{}
	return true
}

// ----------------------------------------

/*
end は、取得対象の実行中状態を解除します。

機能:
  - ミューテックスで実行中マップを保護して ID を削除する

引数:
  - sourceID int64: 実行完了した取得対象 ID

返り値:
  - なし
*/
func (s *Service) end(sourceID int64) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	delete(s.active, sourceID)
}

// ----------------------------------------

/*
normalizeArticle は、記事文字列を正規化して取得対象 ID を設定します。

機能:
  - 識別子、URL、見出し、本文の前後空白を除去する
  - 原データが空の場合は空 JSON オブジェクトを設定する

引数:
  - article domain.Article: コネクタが返した記事
  - sourceID int64: 保存先の取得対象 ID

返り値:
  - domain.Article: 正規化済みの記事
*/
func normalizeArticle(article domain.Article, sourceID int64) domain.Article {
	article.SourceID = sourceID
	article.Provider = strings.TrimSpace(article.Provider)
	article.ExternalID = strings.TrimSpace(article.ExternalID)
	article.RevisionID = strings.TrimSpace(article.RevisionID)
	article.CanonicalURL = strings.TrimSpace(article.CanonicalURL)
	article.Headline = strings.TrimSpace(article.Headline)
	article.BodyText = strings.TrimSpace(article.BodyText)
	if len(article.RawPayload) == 0 {
		article.RawPayload = json.RawMessage(`{}`)
	}
	return article
}

// ----------------------------------------

/*
validateArticle は、DB 保存に必要な記事項目を検証します。

機能:
  - 取得元、記事 ID、リビジョン、見出し、本文の必須性を確認する
  - 原データが有効な JSON であることを確認する

引数:
  - article domain.Article: 検証する記事

返り値:
  - error: 必須値または JSON が不正な場合のエラー
*/
func validateArticle(article domain.Article) error {
	var validationErrors []error
	if article.Provider == "" {
		validationErrors = append(validationErrors, errors.New("provider が空です"))
	}
	if article.ExternalID == "" {
		validationErrors = append(validationErrors, errors.New("external_id が空です"))
	}
	if article.RevisionID == "" {
		validationErrors = append(validationErrors, errors.New("revision_id が空です"))
	}
	if article.Headline == "" {
		validationErrors = append(validationErrors, errors.New("headline が空です"))
	}
	if article.BodyText == "" {
		validationErrors = append(validationErrors, errors.New("body_text が空です"))
	}
	if !json.Valid(article.RawPayload) {
		validationErrors = append(validationErrors, errors.New("raw_payload が JSON ではありません"))
	}
	return errors.Join(validationErrors...)
}

// ----------------------------------------

/*
summarizeWarnings は、警告一覧を DB 保存用の文字列へ変換します。

機能:
  - 空の警告を除去して改行で連結する
  - 保存サイズを抑えるため最大 8000 文字に切り詰める

引数:
  - warnings []string: コネクタまたは保存処理の警告一覧

返り値:
  - string: DB 保存用の警告概要
*/
func summarizeWarnings(warnings []string) string {
	parts := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		if trimmed := strings.TrimSpace(warning); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return truncateMessage(strings.Join(parts, "\n"), 8000)
}

// ----------------------------------------

/*
truncateMessage は、文字列を指定したルーン数以下に切り詰めます。

機能:
  - UTF-8 文字を途中で破壊せずに上限を適用する

引数:
  - value string: 切り詰める文字列
  - maxRunes int: 許容する最大ルーン数

返り値:
  - string: 上限以内へ切り詰めた文字列
*/
func truncateMessage(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
