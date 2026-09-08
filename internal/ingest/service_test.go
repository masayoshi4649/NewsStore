package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/connector"
	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

// fakeRepository は、記事取得サービスの状態遷移をメモリ上で検証します。
type fakeRepository struct {
	mu           sync.Mutex
	source       domain.Source
	nextRunID    int64
	finished     postgres.RunResult
	finishedID   int64
	lastError    string
	insertedKeys map[string]struct{}
}

// fakeFetcher は、テストごとに設定した取得結果またはエラーを返します。
type fakeFetcher struct {
	result  domain.FetchResult
	err     error
	started chan struct{}
	release chan struct{}
}

/*
TestRunSavesArticlesIdempotently は、同じ記事リビジョンが一件だけ新規保存されることを検証します。

機能:
  - 同一記事リビジョンを重複取得しても、一件だけ保存されることを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestRunSavesArticlesIdempotently(t *testing.T) {
	t.Parallel()

	repository := newFakeRepository()
	article := validArticle()
	fetcher := &fakeFetcher{
		result: domain.FetchResult{Articles: []domain.Article{article, article}},
	}
	service := newTestService(repository, fetcher)

	summary, err := service.Run(context.Background(), repository.source.ID, "manual")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Status != postgres.RunStatusSucceeded {
		t.Errorf("Status = %q, 期待値は %q", summary.Status, postgres.RunStatusSucceeded)
	}
	if summary.FetchedCount != 2 || summary.InsertedCount != 1 || summary.SkippedCount != 1 {
		t.Errorf(
			"件数 = fetched:%d inserted:%d skipped:%d, 期待値は 2,1,1",
			summary.FetchedCount,
			summary.InsertedCount,
			summary.SkippedCount,
		)
	}
	if repository.finished.Status != postgres.RunStatusSucceeded {
		t.Errorf("保存状態 = %q, 期待値は %q", repository.finished.Status, postgres.RunStatusSucceeded)
	}
}

// ----------------------------------------

/*
TestRunContinuesAfterInvalidArticle は、不正記事を警告化して有効記事の保存を継続することを検証します。

機能:
  - 不正記事を警告として扱い、後続の有効記事を保存できることを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestRunContinuesAfterInvalidArticle(t *testing.T) {
	t.Parallel()

	repository := newFakeRepository()
	invalid := validArticle()
	invalid.BodyText = " "
	fetcher := &fakeFetcher{
		result: domain.FetchResult{
			Articles: []domain.Article{invalid, validArticle()},
			Warnings: []string{"リンク先本文の取得に失敗しました"},
		},
	}
	service := newTestService(repository, fetcher)

	summary, err := service.Run(context.Background(), repository.source.ID, "scheduled")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Status != postgres.RunStatusPartial {
		t.Errorf("Status = %q, 期待値は %q", summary.Status, postgres.RunStatusPartial)
	}
	if summary.InsertedCount != 1 || summary.WarningCount != 2 {
		t.Errorf(
			"件数 = inserted:%d warnings:%d, 期待値は 1,2",
			summary.InsertedCount,
			summary.WarningCount,
		)
	}
	if repository.lastError == "" {
		t.Error("警告概要が取得対象へ保存されていません")
	}
}

// ----------------------------------------

/*
TestRunRecordsFetcherFailure は、一覧取得失敗が failed 履歴と取得対象へ保存されることを検証します。

機能:
  - コネクタの取得失敗を実行履歴と取得対象のエラー情報へ記録できることを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestRunRecordsFetcherFailure(t *testing.T) {
	t.Parallel()

	repository := newFakeRepository()
	fetcher := &fakeFetcher{err: errors.New("外部 API が利用できません")}
	service := newTestService(repository, fetcher)

	summary, err := service.Run(context.Background(), repository.source.ID, "manual")
	if err == nil {
		t.Fatal("Run() error = nil, 取得失敗エラーを期待")
	}
	if summary.Status != postgres.RunStatusFailed {
		t.Errorf("Status = %q, 期待値は %q", summary.Status, postgres.RunStatusFailed)
	}
	if repository.finished.Status != postgres.RunStatusFailed {
		t.Errorf("保存状態 = %q, 期待値は %q", repository.finished.Status, postgres.RunStatusFailed)
	}
	if repository.lastError == "" {
		t.Error("失敗概要が取得対象へ保存されていません")
	}
}

// ----------------------------------------

/*
TestRunRejectsConcurrentSource は、同じ取得対象の同時実行を拒否することを検証します。

機能:
  - 実行中の取得対象に対する重複実行が排除されることを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestRunRejectsConcurrentSource(t *testing.T) {
	t.Parallel()

	repository := newFakeRepository()
	fetcher := &fakeFetcher{
		result:  domain.FetchResult{Articles: []domain.Article{validArticle()}},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := newTestService(repository, fetcher)

	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Run(context.Background(), repository.source.ID, "manual")
		firstDone <- err
	}()
	<-fetcher.started

	if _, err := service.Run(context.Background(), repository.source.ID, "manual"); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("同時 Run() error = %v, 期待値は ErrAlreadyRunning", err)
	}
	close(fetcher.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("先行 Run() error = %v", err)
	}
}

// ----------------------------------------

/*
newFakeRepository は、テスト用取得対象を持つメモリ Repository を生成します。

機能:
  - 記事取得サービスのテストで使用する初期状態の偽 Repository を構築します。

引数:
  - なし

返り値:
  - *fakeRepository: 初期化済みのテスト Repository
*/
func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		source: domain.Source{
			ID:                   1,
			Name:                 "テストフィード",
			Kind:                 domain.SourceKindRSS,
			EndpointURL:          "https://example.com/feed.xml",
			Enabled:              true,
			FetchIntervalMinutes: 15,
			MaxItems:             20,
			Settings:             json.RawMessage(`{}`),
		},
		nextRunID:    1,
		insertedKeys: make(map[string]struct{}),
	}
}

// ----------------------------------------

/*
newTestService は、偽 Repository と偽コネクタから記事取得サービスを生成します。

機能:
  - 外部入出力を偽実装へ置き換えたテスト用 Service を構築します。

引数:
  - repository *fakeRepository: 実行状態を記録するメモリ Repository
  - fetcher *fakeFetcher: 取得結果を返す偽コネクタ

返り値:
  - *Service: テスト用の記事取得サービス
*/
func newTestService(repository *fakeRepository, fetcher *fakeFetcher) *Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(repository, connector.NewRegistry(fetcher, fetcher, fetcher), logger)
}

// ----------------------------------------

/*
validArticle は、保存要件を満たすテスト記事を生成します。

機能:
  - 記事保存処理へ入力できる必須項目設定済みの記事を生成します。

引数:
  - なし

返り値:
  - domain.Article: 必須項目を持つ記事
*/
func validArticle() domain.Article {
	publishedAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	return domain.Article{
		Provider:     "rss",
		ExternalID:   "article-1",
		RevisionID:   "revision-1",
		CanonicalURL: "https://example.com/articles/1",
		PublishedAt:  &publishedAt,
		Headline:     "テスト記事",
		BodyText:     "テスト本文です。",
		RawPayload:   json.RawMessage(`{"id":"article-1"}`),
	}
}

// ----------------------------------------

/*
GetSource は、テスト用取得対象を返します。

機能:
  - 指定 ID に一致する取得対象を偽 Repository から返します。

引数:
  - ctx context.Context: 未使用の DB 操作コンテキスト
  - id int64: 取得対象 ID

返り値:
  - domain.Source: テスト用取得対象
  - error: ID が一致しない場合のエラー
*/
func (r *fakeRepository) GetSource(_ context.Context, id int64) (domain.Source, error) {
	if id != r.source.ID {
		return domain.Source{}, postgres.ErrNotFound
	}
	return r.source, nil
}

// ----------------------------------------

/*
StartRun は、テスト用の実行 ID を採番します。

機能:
  - 排他制御しながら連番の実行 ID を払い出します。

引数:
  - ctx context.Context: 未使用の DB 操作コンテキスト
  - sourceID int64: 未使用の取得対象 ID
  - triggerType string: 未使用の起動方法

返り値:
  - int64: 採番した実行 ID
  - error: 常に nil
*/
func (r *fakeRepository) StartRun(_ context.Context, _ int64, _ string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextRunID
	r.nextRunID++
	return id, nil
}

// ----------------------------------------

/*
FinishRun は、テスト用の最終実行結果を記録します。

機能:
  - 完了した実行 ID と最終結果を偽 Repository の状態へ保存します。

引数:
  - ctx context.Context: 未使用の DB 操作コンテキスト
  - id int64: 完了した実行 ID
  - result postgres.RunResult: 記録する最終結果

返り値:
  - error: 常に nil
*/
func (r *fakeRepository) FinishRun(_ context.Context, id int64, result postgres.RunResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishedID = id
	r.finished = result
	return nil
}

// ----------------------------------------

/*
FinishRunAndSource は、テスト用の最終結果と取得対象エラーを同時に記録します。

機能:
  - 実行結果と取得対象へ反映するエラー概要を同じメモリ状態へ保存します。

引数:
  - ctx context.Context: 未使用のDB操作コンテキスト
  - runID int64: 完了した実行ID
  - sourceID int64: 未使用の取得対象ID
  - result postgres.RunResult: 記録する最終結果

返り値:
  - error: 常にnil
*/
func (r *fakeRepository) FinishRunAndSource(
	_ context.Context,
	runID int64,
	_ int64,
	result postgres.RunResult,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishedID = runID
	r.finished = result
	r.lastError = result.ErrorMessage
	return nil
}

// ----------------------------------------

/*
InsertArticle は、複合キーを使ってメモリ上の冪等保存を行います。

機能:
  - 外部記事 ID とリビジョン ID の組み合わせで重複を判定して記事を保存します。

引数:
  - ctx context.Context: 未使用の DB 操作コンテキスト
  - article domain.Article: 保存する記事

返り値:
  - bool: 初回保存の場合は true、重複時は false
  - error: 常に nil
*/
func (r *fakeRepository) InsertArticle(_ context.Context, article domain.Article) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := article.ExternalID + "\x00" + article.RevisionID
	if _, exists := r.insertedKeys[key]; exists {
		return false, nil
	}
	r.insertedKeys[key] = struct{}{}
	return true, nil
}

// ----------------------------------------

/*
MarkSourceFinished は、テスト用の最終エラー概要を記録します。

機能:
  - 取得処理の完了時にエラーまたは警告の概要を偽 Repository へ保存します。

引数:
  - ctx context.Context: 未使用の DB 操作コンテキスト
  - id int64: 未使用の取得対象 ID
  - lastError string: 記録するエラーまたは警告概要

返り値:
  - error: 常に nil
*/
func (r *fakeRepository) MarkSourceFinished(_ context.Context, _ int64, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastError = lastError
	return nil
}

// ----------------------------------------

/*
AcquireSourceLock は、テスト用の取得済み排他ロックと解放関数を返します。

機能:
  - ロック取得成功を模擬し、常に成功する解放関数を提供します。

引数:
  - ctx context.Context: 未使用のロック取得コンテキスト
  - sourceID int64: 未使用の取得対象ID

返り値:
  - func(context.Context) error: 常に成功する解放関数
  - bool: 常にtrue
  - error: 常にnil
*/
func (r *fakeRepository) AcquireSourceLock(
	_ context.Context,
	_ int64,
) (func(context.Context) error, bool, error) {
	release := func(context.Context) error {
		return nil
	}
	return release, true, nil
}

// ----------------------------------------

/*
Fetch は、設定された取得結果を返し、必要に応じてテストを待機させます。

機能:
  - 事前設定した取得結果またはエラーを返し、同時実行テストでは解放通知まで待機します。

引数:
  - ctx context.Context: 待機中のキャンセルを受け取るコンテキスト
  - source domain.Source: 未使用の取得対象

返り値:
  - domain.FetchResult: テストで設定した取得結果
  - error: テストで設定したエラーまたはキャンセルエラー
*/
func (f *fakeFetcher) Fetch(ctx context.Context, _ domain.Source) (domain.FetchResult, error) {
	if f.started != nil {
		close(f.started)
		select {
		case <-ctx.Done():
			return domain.FetchResult{}, ctx.Err()
		case <-f.release:
		}
	}
	return f.result, f.err
}
