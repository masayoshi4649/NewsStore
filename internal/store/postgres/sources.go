package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
)

const sourceColumns = `
	id,
	name,
	kind,
	endpoint_url,
	enabled,
	fetch_interval_minutes,
	max_items,
	settings,
	last_fetched_at,
	next_fetch_at,
	last_error,
	created_at,
	updated_at`

// rowScanner は、sql.Row と sql.Rows に共通する Scan 操作を表します。
type rowScanner interface {
	Scan(dest ...any) error
}

/*
ListSources は、論理削除されていない取得対象を一覧します。

機能:
  - 取得対象を有効状態、名前、ID の順で並べる
  - コネクタ固有設定を JSON のまま返す

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト

返り値:
  - []domain.Source: 取得対象の一覧
  - error: DB 問い合わせまたは行読込に失敗した場合のエラー
*/
func (s *Store) ListSources(ctx context.Context) ([]domain.Source, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT `+sourceColumns+`
		FROM news_sources
		WHERE deleted_at IS NULL
		ORDER BY enabled DESC, LOWER(name), id`,
	)
	if err != nil {
		return nil, fmt.Errorf("取得対象を一覧できません: %w", err)
	}
	defer rows.Close()

	sources := make([]domain.Source, 0)
	for rows.Next() {
		source, scanErr := scanSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("取得対象一覧を読み込めません: %w", err)
	}
	return sources, nil
}

// ----------------------------------------

/*
ListDueSourceIDs は、定期取得の期限が到来した取得対象 ID を返します。

機能:
  - 有効かつ未削除で next_fetch_at が到来した行だけを選ぶ
  - 古い予定から指定件数まで返す

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - limit int: 返す最大件数

返り値:
  - []int64: 取得期限が到来した取得対象 ID
  - error: DB 問い合わせまたは行読込に失敗した場合のエラー
*/
func (s *Store) ListDueSourceIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id
		FROM news_sources
		WHERE enabled = TRUE
			AND deleted_at IS NULL
			AND (next_fetch_at IS NULL OR next_fetch_at <= CURRENT_TIMESTAMP)
		ORDER BY next_fetch_at NULLS FIRST, id
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("期限到来した取得対象を一覧できません: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("取得対象 ID を読み込めません: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("取得対象 ID 一覧を読み込めません: %w", err)
	}
	return ids, nil
}

// ----------------------------------------

/*
GetSource は、指定 ID の取得対象を返します。

機能:
  - 論理削除されていない取得対象を ID で検索する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - id int64: 取得対象 ID

返り値:
  - domain.Source: 検索した取得対象
  - error: 対象がない場合または DB 操作に失敗した場合のエラー
*/
func (s *Store) GetSource(ctx context.Context, id int64) (domain.Source, error) {
	source, err := scanSource(s.db.QueryRowContext(
		ctx,
		`SELECT `+sourceColumns+`
		FROM news_sources
		WHERE id = $1 AND deleted_at IS NULL`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Source{}, ErrNotFound
	}
	if err != nil {
		return domain.Source{}, err
	}
	return source, nil
}

// ----------------------------------------

/*
CreateSource は、新しい取得対象を PostgreSQL へ保存します。

機能:
  - 共通設定とコネクタ固有 JSON を保存する
  - 有効な取得対象は直ちに定期取得対象になるよう次回日時を設定する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - source domain.Source: 保存する取得対象

返り値:
  - domain.Source: DB が採番・設定した値を含む取得対象
  - error: INSERT または保存後の読込に失敗した場合のエラー
*/
func (s *Store) CreateSource(ctx context.Context, source domain.Source) (domain.Source, error) {
	var id int64
	err := s.db.QueryRowContext(
		ctx,
		`INSERT INTO news_sources (
			name,
			kind,
			endpoint_url,
			enabled,
			fetch_interval_minutes,
			max_items,
			settings,
			next_fetch_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			CASE WHEN $4 THEN CURRENT_TIMESTAMP ELSE NULL END
		)
		RETURNING id`,
		source.Name,
		source.Kind,
		source.EndpointURL,
		source.Enabled,
		source.FetchIntervalMinutes,
		source.MaxItems,
		string(source.Settings),
	).Scan(&id)
	if err != nil {
		return domain.Source{}, fmt.Errorf("取得対象を作成できません: %w", err)
	}
	return s.GetSource(ctx, id)
}

// ----------------------------------------

/*
UpdateSource は、既存の取得対象設定を更新します。

機能:
  - 指定 ID の共通設定とコネクタ固有 JSON を置き換える
  - 有効化時は次回日時がない場合に直ちに取得予定とする
  - 無効化時は次回日時を空にする

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - source domain.Source: ID と更新後の値を持つ取得対象

返り値:
  - domain.Source: 更新後の取得対象
  - error: 対象がない場合または UPDATE に失敗した場合のエラー
*/
func (s *Store) UpdateSource(ctx context.Context, source domain.Source) (domain.Source, error) {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE news_sources
		SET name = $2,
			kind = $3,
			endpoint_url = $4,
			enabled = $5,
			fetch_interval_minutes = $6,
			max_items = $7,
			settings = $8,
			next_fetch_at = CASE
				WHEN $5 THEN COALESCE(next_fetch_at, CURRENT_TIMESTAMP)
				ELSE NULL
			END,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND deleted_at IS NULL`,
		source.ID,
		source.Name,
		source.Kind,
		source.EndpointURL,
		source.Enabled,
		source.FetchIntervalMinutes,
		source.MaxItems,
		string(source.Settings),
	)
	if err != nil {
		return domain.Source{}, fmt.Errorf("取得対象を更新できません: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Source{}, fmt.Errorf("取得対象の更新件数を確認できません: %w", err)
	}
	if affected == 0 {
		return domain.Source{}, ErrNotFound
	}
	return s.GetSource(ctx, source.ID)
}

// ----------------------------------------

/*
DeleteSource は、指定 ID の取得対象を論理削除します。

機能:
  - 記事と取得履歴を残したまま取得対象を無効化する
  - 次回取得予定を空にする

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - id int64: 論理削除する取得対象 ID

返り値:
  - error: 対象がない場合または UPDATE に失敗した場合のエラー
*/
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE news_sources
		SET enabled = FALSE,
			next_fetch_at = NULL,
			deleted_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND deleted_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("取得対象を削除できません: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("取得対象の削除件数を確認できません: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// ----------------------------------------

/*
MarkSourceFinished は、取得対象の実行完了状態と次回予定を更新します。

機能:
  - 最終実行日時を現在時刻へ更新する
  - 成否に応じたエラー内容を保存する
  - 有効な取得対象の次回日時を取得間隔から計算する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - id int64: 実行を完了した取得対象 ID
  - lastError string: 成功時は空、失敗時は利用者向けエラー概要

返り値:
  - error: UPDATE に失敗した場合のエラー
*/
func (s *Store) MarkSourceFinished(ctx context.Context, id int64, lastError string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE news_sources
		SET last_fetched_at = CURRENT_TIMESTAMP,
			last_error = $2,
			next_fetch_at = CASE
				WHEN enabled THEN CURRENT_TIMESTAMP + (fetch_interval_minutes * INTERVAL '1 minute')
				ELSE NULL
			END,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND deleted_at IS NULL`,
		id,
		lastError,
	)
	if err != nil {
		return fmt.Errorf("取得対象の実行状態を更新できません: %w", err)
	}
	return nil
}

// ----------------------------------------

/*
scanSource は、DB 行を共通取得対象モデルへ変換します。

機能:
  - PostgreSQL の各列を domain.Source へ読み込む

引数:
  - scanner rowScanner: sql.Row または sql.Rows

返り値:
  - domain.Source: 読み込んだ取得対象
  - error: 列の読込に失敗した場合のエラー
*/
func scanSource(scanner rowScanner) (domain.Source, error) {
	var source domain.Source
	err := scanner.Scan(
		&source.ID,
		&source.Name,
		&source.Kind,
		&source.EndpointURL,
		&source.Enabled,
		&source.FetchIntervalMinutes,
		&source.MaxItems,
		&source.Settings,
		&source.LastFetchedAt,
		&source.NextFetchAt,
		&source.LastError,
		&source.CreatedAt,
		&source.UpdatedAt,
	)
	if err != nil {
		return domain.Source{}, fmt.Errorf("取得対象を読み込めません: %w", err)
	}
	return source, nil
}

// ----------------------------------------

/*
NextFetchTime は、取得間隔から次回日時を計算します。

機能:
  - テスト可能な形で分単位の取得間隔を日時へ加算する

引数:
  - base time.Time: 計算基準日時
  - minutes int: 取得間隔の分数

返り値:
  - time.Time: 計算した次回取得日時
*/
func NextFetchTime(base time.Time, minutes int) time.Time {
	return base.Add(time.Duration(minutes) * time.Minute)
}
