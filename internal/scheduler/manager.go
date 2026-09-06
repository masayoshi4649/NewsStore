// Package scheduler は、定期取得と管理画面からの手動取得キューを管理します。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/ingest"
)

// DueSourceRepository は、定期取得対象の確認と期限切れ記事の削除を行います。
type DueSourceRepository interface {
	ListDueSourceIDs(ctx context.Context, limit int) ([]int64, error)
	DeleteExpiredArticles(ctx context.Context) (int64, error)
}

// Runner は、キューから受け取った取得対象を実行します。
type Runner interface {
	Run(ctx context.Context, sourceID int64, triggerType string) (ingest.Summary, error)
}

// Job は、一回の取得キュー項目を表します。
type Job struct {
	SourceID    int64
	TriggerType string
}

// Manager は、期限確認、重複しないキュー、並列ワーカーを管理します。
type Manager struct {
	repository              DueSourceRepository
	runner                  Runner
	pollInterval            time.Duration
	retentionInterval       time.Duration
	articleRetentionEnabled bool
	logger                  *slog.Logger
	jobs                    chan Job
	pendingMu               sync.Mutex
	pending                 map[int64]struct{}
}

/*
New は、記事取得スケジューラーを生成します。

機能:
  - 期限確認間隔、ワーカー数に応じたキュー容量を設定する
  - 同じ取得対象の重複キューを防ぐ管理領域を初期化する

引数:
  - repository DueSourceRepository: 期限到来した取得対象の確認と記事削除を行う Repository
  - runner Runner: 記事取得を実行するサービス
  - pollInterval time.Duration: DB の期限を確認する間隔
  - concurrency int: 同時実行するワーカー数
  - logger *slog.Logger: 実行状態を記録するロガー

返り値:
  - *Manager: 初期化済みのスケジューラー
*/
func New(
	repository DueSourceRepository,
	runner Runner,
	pollInterval time.Duration,
	concurrency int,
	logger *slog.Logger,
) *Manager {
	if pollInterval <= 0 {
		pollInterval = time.Minute
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Manager{
		repository:        repository,
		runner:            runner,
		pollInterval:      pollInterval,
		retentionInterval: time.Hour,
		logger:            logger,
		jobs:              make(chan Job, concurrency*8),
		pending:           make(map[int64]struct{}),
	}
}

// ----------------------------------------

/*
SetArticleRetentionEnabled は、保持期間を過ぎた記事の自動削除を切り替えます。

機能:
  - 既定で無効の記事削除を設定値に応じて有効化または無効化する
  - Run を開始する前に呼び出し、実行中は設定を変更しない

引数:
  - enabled bool: 自動削除を有効にする場合は true

返り値:
  - なし
*/
func (m *Manager) SetArticleRetentionEnabled(enabled bool) {
	m.articleRetentionEnabled = enabled
}

// ----------------------------------------

/*
Run は、期限確認ループと指定数のワーカーを開始します。

機能:
  - 開始直後と一定間隔ごとに期限到来した取得対象をキューへ入れる
  - 自動削除が有効な場合だけ、開始直後と 1 時間ごとに保持期間を過ぎた記事を削除する
  - コンテキスト終了までワーカーを稼働する
  - 全ワーカー終了後に返る

引数:
  - ctx context.Context: スケジューラー全体の終了を伝えるコンテキスト
  - concurrency int: 起動するワーカー数

返り値:
  - なし
*/
func (m *Manager) Run(ctx context.Context, concurrency int) {
	if concurrency < 1 {
		concurrency = 1
	}

	var workers sync.WaitGroup
	for index := 0; index < concurrency; index++ {
		workers.Add(1)
		go func(workerIndex int) {
			defer workers.Done()
			m.runWorker(ctx, workerIndex)
		}(index + 1)
	}

	var retentionTicks <-chan time.Time
	if m.articleRetentionEnabled {
		m.deleteExpiredArticles(ctx)
		retentionTicker := time.NewTicker(m.retentionInterval)
		defer retentionTicker.Stop()
		retentionTicks = retentionTicker.C
	}
	m.enqueueDue(ctx)
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return
		case <-ticker.C:
			m.enqueueDue(ctx)
		case <-retentionTicks:
			m.deleteExpiredArticles(ctx)
		}
	}
}

// ----------------------------------------

/*
deleteExpiredArticles は、期限を制限して保持期間を過ぎた記事を削除します。

機能:
  - 取得対象の有無や記事取得の成否にかかわらず記事の保持期限を適用する
  - 削除処理の上限を 1 分に設定し、失敗してもスケジューラーを継続する
  - 削除した記事が存在する場合は件数を記録する

引数:
  - ctx context.Context: スケジューラー終了を伝えるコンテキスト

返り値:
  - なし
*/
func (m *Manager) deleteExpiredArticles(ctx context.Context) {
	cleanupCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	deleted, err := m.repository.DeleteExpiredArticles(cleanupCtx)
	if err != nil {
		m.logger.Error("保持期間を過ぎた記事を削除できません", "error", err)
		return
	}
	if deleted > 0 {
		m.logger.Info("保持期間を過ぎた記事を削除しました", "deleted_count", deleted)
	}
}

// ----------------------------------------

/*
Enqueue は、取得対象を重複しないようキューへ追加します。

機能:
  - 既に待機中または実行中の ID を再追加しない
  - キュー満杯時は pending 状態を元へ戻す

引数:
  - sourceID int64: キューへ追加する取得対象 ID
  - triggerType string: scheduled、manual、cli のいずれか

返り値:
  - bool: 新たにキューへ追加した場合は true、重複時は false
  - error: ID、起動方法、またはキュー容量が不正な場合のエラー
*/
func (m *Manager) Enqueue(sourceID int64, triggerType string) (bool, error) {
	if sourceID < 1 {
		return false, errors.New("取得対象 ID は 1 以上が必要です")
	}
	if triggerType != "scheduled" && triggerType != "manual" && triggerType != "cli" {
		return false, fmt.Errorf("不正な取得起動方法です: %q", triggerType)
	}

	m.pendingMu.Lock()
	if _, exists := m.pending[sourceID]; exists {
		m.pendingMu.Unlock()
		return false, nil
	}
	m.pending[sourceID] = struct{}{}
	m.pendingMu.Unlock()

	select {
	case m.jobs <- Job{SourceID: sourceID, TriggerType: triggerType}:
		return true, nil
	default:
		m.complete(sourceID)
		return false, errors.New("記事取得キューが満杯です")
	}
}

// ----------------------------------------

/*
enqueueDue は、期限到来した取得対象を定期実行キューへ追加します。

機能:
  - Repository から最大 1000 件の対象 ID を取得する
  - 重複キューは正常系として無視する

引数:
  - ctx context.Context: DB 問い合わせの期限とキャンセルを伝えるコンテキスト

返り値:
  - なし
*/
func (m *Manager) enqueueDue(ctx context.Context) {
	ids, err := m.repository.ListDueSourceIDs(ctx, 1000)
	if err != nil {
		m.logger.Error("定期取得対象を確認できません", "error", err)
		return
	}

	for _, id := range ids {
		if _, err := m.Enqueue(id, "scheduled"); err != nil {
			m.logger.Error("定期取得をキューへ追加できません", "source_id", id, "error", err)
			return
		}
	}
}

// ----------------------------------------

/*
runWorker は、キューから取得対象を受け取って順番に実行します。

機能:
  - コンテキスト終了までジョブを待機する
  - 実行完了後に必ず pending 状態を解除する

引数:
  - ctx context.Context: ワーカー終了と記事取得へ伝えるコンテキスト
  - workerIndex int: ログ識別用のワーカー番号

返り値:
  - なし
*/
func (m *Manager) runWorker(ctx context.Context, workerIndex int) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-m.jobs:
			if ctx.Err() != nil {
				m.complete(job.SourceID)
				return
			}
			_, err := m.runner.Run(ctx, job.SourceID, job.TriggerType)
			m.complete(job.SourceID)
			if err != nil && !errors.Is(err, ingest.ErrAlreadyRunning) {
				m.logger.Error(
					"記事取得ワーカーが失敗しました",
					"worker", workerIndex,
					"source_id", job.SourceID,
					"error", err,
				)
			}
		}
	}
}

// ----------------------------------------

/*
complete は、取得対象の待機中または実行中状態を解除します。

機能:
  - pending マップから指定 ID を安全に削除する

引数:
  - sourceID int64: 完了した取得対象 ID

返り値:
  - なし
*/
func (m *Manager) complete(sourceID int64) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	delete(m.pending, sourceID)
}
