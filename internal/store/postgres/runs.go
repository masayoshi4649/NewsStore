package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RunStatus は、記事取得実行の状態を表します。
type RunStatus string

const (
	// RunStatusRunning は、記事取得を実行中であることを表します。
	RunStatusRunning RunStatus = "running"
	// RunStatusSucceeded は、警告なく記事取得が完了したことを表します。
	RunStatusSucceeded RunStatus = "succeeded"
	// RunStatusPartial は、一部警告を伴って記事取得が完了したことを表します。
	RunStatusPartial RunStatus = "partial"
	// RunStatusFailed は、記事取得全体が失敗したことを表します。
	RunStatusFailed RunStatus = "failed"
)

// RunResult は、記事取得の件数と最終状態を表します。
type RunResult struct {
	Status        RunStatus
	FetchedCount  int
	InsertedCount int
	SkippedCount  int
	WarningCount  int
	ErrorMessage  string
}

// IngestionRun は、管理画面へ表示する記事取得履歴を表します。
type IngestionRun struct {
	ID            int64
	SourceID      int64
	SourceName    string
	TriggerType   string
	Status        RunStatus
	StartedAt     time.Time
	FinishedAt    *time.Time
	FetchedCount  int
	InsertedCount int
	SkippedCount  int
	WarningCount  int
	ErrorMessage  string
}

/*
RecoverStaleRuns は、プロセス異常終了で残った古いrunning履歴をfailedへ更新します。

機能:
  - 指定時間より前に開始したrunning履歴だけを対象にする
  - 取得元のadvisory lockを取得できる場合だけ異常終了済みと判定する
  - 別プロセスが正常に長時間実行中の履歴は更新しない
  - 完了日時と回復理由を保存して管理画面の永続的な実行中表示を防ぐ

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - olderThan time.Duration: staleと判定する開始後の経過時間

返り値:
  - int64: failedへ回復した取得履歴の件数
  - error: 引数が不正またはUPDATEに失敗した場合のエラー
*/
func (s *Store) RecoverStaleRuns(
	ctx context.Context,
	olderThan time.Duration,
) (int64, error) {
	if olderThan <= 0 {
		return 0, errors.New("stale判定時間は0より大きい必要があります")
	}
	seconds := int64(olderThan / time.Second)

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT DISTINCT source_id
		FROM ingestion_runs
		WHERE status = 'running'
			AND started_at < CURRENT_TIMESTAMP - ($1 * INTERVAL '1 second')
		ORDER BY source_id`,
		seconds,
	)
	if err != nil {
		return 0, fmt.Errorf("回復対象の取得元を一覧できません: %w", err)
	}
	defer rows.Close()

	sourceIDs := make([]int64, 0)
	for rows.Next() {
		var sourceID int64
		if err := rows.Scan(&sourceID); err != nil {
			return 0, fmt.Errorf("回復対象の取得元IDを読み込めません: %w", err)
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("回復対象の取得元一覧を読み込めません: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("回復対象の取得元一覧を閉じられません: %w", err)
	}

	var recovered int64
	for _, sourceID := range sourceIDs {
		affected, recoverErr := s.recoverStaleRunsForSource(
			ctx,
			sourceID,
			seconds,
		)
		if recoverErr != nil {
			return recovered, recoverErr
		}
		recovered += affected
	}
	return recovered, nil
}

// ----------------------------------------

/*
recoverStaleRunsForSource は、実行ロックが空いている取得元の古い履歴を回復します。

機能:
  - 取得元単位のadvisory lockを非待機で試行する
  - 他プロセスが取得中の場合は正常な長時間実行とみなして更新しない
  - ロック取得後もrunningかつ期限超過の履歴だけをfailedへ更新する
  - 更新成否にかかわらず、独立した期限でadvisory lockを解放する

引数:
  - ctx context.Context: DB操作の期限とキャンセルを伝えるコンテキスト
  - sourceID int64: 回復を試行する取得元ID
  - olderThanSeconds int64: staleと判定する開始後の秒数

返り値:
  - int64: failedへ回復した取得履歴の件数
  - error: ロック、UPDATE、件数確認、またはロック解放に失敗した場合のエラー
*/
func (s *Store) recoverStaleRunsForSource(
	ctx context.Context,
	sourceID int64,
	olderThanSeconds int64,
) (int64, error) {
	release, acquired, err := s.AcquireSourceLock(ctx, sourceID)
	if err != nil {
		return 0, fmt.Errorf("取得元%dの回復ロックを取得できません: %w", sourceID, err)
	}
	if !acquired {
		return 0, nil
	}
	if release == nil {
		return 0, fmt.Errorf("取得元%dの回復ロック解放関数がありません", sourceID)
	}

	result, updateErr := s.db.ExecContext(
		ctx,
		`UPDATE ingestion_runs
		SET status = 'failed',
			finished_at = CURRENT_TIMESTAMP,
			warning_count = warning_count + 1,
			error_message = CASE
				WHEN error_message = '' THEN '前回プロセスの異常終了後に回復しました'
				ELSE error_message || E'\n前回プロセスの異常終了後に回復しました'
			END
		WHERE source_id = $2
			AND status = 'running'
			AND started_at < CURRENT_TIMESTAMP - ($1 * INTERVAL '1 second')`,
		olderThanSeconds,
		sourceID,
	)

	var affected int64
	if updateErr == nil {
		affected, updateErr = result.RowsAffected()
		if updateErr != nil {
			updateErr = fmt.Errorf(
				"取得元%dで回復した履歴件数を確認できません: %w",
				sourceID,
				updateErr,
			)
		}
	} else {
		updateErr = fmt.Errorf("取得元%dの古い取得履歴を回復できません: %w", sourceID, updateErr)
	}

	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	releaseErr := release(releaseCtx)
	cancel()
	if releaseErr != nil {
		releaseErr = fmt.Errorf("取得元%dの回復ロックを解放できません: %w", sourceID, releaseErr)
	}

	return affected, errors.Join(updateErr, releaseErr)
}

// ----------------------------------------

/*
StartRun は、記事取得の実行開始履歴を作成します。

機能:
  - 取得対象と起動方法を running 状態で記録する
  - 採番された実行 ID を返す

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - sourceID int64: 取得対象 ID
  - triggerType string: scheduled、manual、cli のいずれか

返り値:
  - int64: 作成した取得実行 ID
  - error: INSERT に失敗した場合のエラー
*/
func (s *Store) StartRun(ctx context.Context, sourceID int64, triggerType string) (int64, error) {
	var id int64
	if err := s.db.QueryRowContext(
		ctx,
		`INSERT INTO ingestion_runs (source_id, trigger_type, status)
		VALUES ($1, $2, 'running')
		RETURNING id`,
		sourceID,
		triggerType,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("取得履歴を開始できません: %w", err)
	}
	return id, nil
}

// ----------------------------------------

/*
FinishRun は、記事取得履歴を最終結果で更新します。

機能:
  - 状態、完了日時、各件数、エラー概要を保存する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - id int64: 取得実行 ID
  - result RunResult: 保存する最終状態と件数

返り値:
  - error: UPDATE に失敗した場合のエラー
*/
func (s *Store) FinishRun(ctx context.Context, id int64, result RunResult) error {
	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE ingestion_runs
		SET status = $2,
			finished_at = CURRENT_TIMESTAMP,
			fetched_count = $3,
			inserted_count = $4,
			skipped_count = $5,
			warning_count = $6,
			error_message = $7
		WHERE id = $1`,
		id,
		result.Status,
		result.FetchedCount,
		result.InsertedCount,
		result.SkippedCount,
		result.WarningCount,
		result.ErrorMessage,
	); err != nil {
		return fmt.Errorf("取得履歴を完了できません: %w", err)
	}
	return nil
}

// ----------------------------------------

/*
FinishRunAndSource は、取得履歴と取得対象の次回予定を同一トランザクションで更新します。

機能:
  - 取得履歴の状態、完了日時、件数、エラー概要を保存する
  - 取得対象の最終実行、最終エラー、次回予定を保存する
  - 片方が失敗した場合は両方の更新をロールバックする

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - runID int64: 完了する取得実行 ID
  - sourceID int64: 次回予定を更新する取得対象 ID
  - result RunResult: 保存する最終状態、件数、エラー概要

返り値:
  - error: トランザクション開始、UPDATE、または確定に失敗した場合のエラー
*/
func (s *Store) FinishRunAndSource(
	ctx context.Context,
	runID int64,
	sourceID int64,
	result RunResult,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("取得完了トランザクションを開始できません: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	runUpdate, err := tx.ExecContext(
		ctx,
		`UPDATE ingestion_runs
		SET status = $2,
			finished_at = CURRENT_TIMESTAMP,
			fetched_count = $3,
			inserted_count = $4,
			skipped_count = $5,
			warning_count = $6,
			error_message = $7
		WHERE id = $1`,
		runID,
		result.Status,
		result.FetchedCount,
		result.InsertedCount,
		result.SkippedCount,
		result.WarningCount,
		result.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("取得履歴を完了できません: %w", err)
	}
	runRows, err := runUpdate.RowsAffected()
	if err != nil {
		return fmt.Errorf("取得履歴の更新件数を確認できません: %w", err)
	}
	if runRows != 1 {
		return fmt.Errorf("完了対象の取得履歴が見つかりません: run_id=%d", runID)
	}

	sourceUpdate, err := tx.ExecContext(
		ctx,
		`UPDATE news_sources
		SET last_fetched_at = CURRENT_TIMESTAMP,
			last_error = $2,
			next_fetch_at = CASE
				WHEN enabled THEN CURRENT_TIMESTAMP + (fetch_interval_minutes * INTERVAL '1 minute')
				ELSE NULL
			END,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1`,
		sourceID,
		result.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("取得対象の実行状態を更新できません: %w", err)
	}
	sourceRows, err := sourceUpdate.RowsAffected()
	if err != nil {
		return fmt.Errorf("取得対象の更新件数を確認できません: %w", err)
	}
	if sourceRows != 1 {
		return fmt.Errorf("完了対象の取得対象が見つかりません: source_id=%d", sourceID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("取得完了トランザクションを確定できません: %w", err)
	}
	return nil
}

// ----------------------------------------

/*
ListRuns は、新しい順に記事取得履歴を一覧します。

機能:
  - 取得対象名を結合して指定件数まで返す

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - limit int: 返す最大件数

返り値:
  - []IngestionRun: 記事取得履歴の一覧
  - error: DB 問い合わせまたは行読込に失敗した場合のエラー
*/
func (s *Store) ListRuns(ctx context.Context, limit int) ([]IngestionRun, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT
			r.id,
			r.source_id,
			s.name,
			r.trigger_type,
			r.status,
			r.started_at,
			r.finished_at,
			r.fetched_count,
			r.inserted_count,
			r.skipped_count,
			r.warning_count,
			r.error_message
		FROM ingestion_runs r
		INNER JOIN news_sources s ON s.id = r.source_id
		ORDER BY r.started_at DESC, r.id DESC
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("取得履歴を一覧できません: %w", err)
	}
	defer rows.Close()

	runs := make([]IngestionRun, 0, limit)
	for rows.Next() {
		run, scanErr := scanRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("取得履歴一覧を読み込めません: %w", err)
	}
	return runs, nil
}

// ----------------------------------------

/*
GetRun は、指定 ID の記事取得履歴を返します。

機能:
  - 取得対象名を結合して単一の履歴を読み込む

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - id int64: 取得実行 ID

返り値:
  - IngestionRun: 指定した取得履歴
  - error: 対象がない場合または DB 問い合わせに失敗した場合のエラー
*/
func (s *Store) GetRun(ctx context.Context, id int64) (IngestionRun, error) {
	run, err := scanRun(s.db.QueryRowContext(
		ctx,
		`SELECT
			r.id,
			r.source_id,
			s.name,
			r.trigger_type,
			r.status,
			r.started_at,
			r.finished_at,
			r.fetched_count,
			r.inserted_count,
			r.skipped_count,
			r.warning_count,
			r.error_message
		FROM ingestion_runs r
		INNER JOIN news_sources s ON s.id = r.source_id
		WHERE r.id = $1`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return IngestionRun{}, ErrNotFound
	}
	if err != nil {
		return IngestionRun{}, err
	}
	return run, nil
}

// ----------------------------------------

/*
scanRun は、DB 行を記事取得履歴へ変換します。

機能:
  - 取得対象名と履歴列を IngestionRun へ読み込む

引数:
  - scanner rowScanner: sql.Row または sql.Rows

返り値:
  - IngestionRun: 読み込んだ取得履歴
  - error: 列の読込に失敗した場合のエラー
*/
func scanRun(scanner rowScanner) (IngestionRun, error) {
	var run IngestionRun
	err := scanner.Scan(
		&run.ID,
		&run.SourceID,
		&run.SourceName,
		&run.TriggerType,
		&run.Status,
		&run.StartedAt,
		&run.FinishedAt,
		&run.FetchedCount,
		&run.InsertedCount,
		&run.SkippedCount,
		&run.WarningCount,
		&run.ErrorMessage,
	)
	if err != nil {
		return IngestionRun{}, fmt.Errorf("取得履歴を読み込めません: %w", err)
	}
	return run, nil
}
