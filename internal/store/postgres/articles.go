package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
)

// StoredArticle は、DB の識別子と取得対象名を付加した保存済み記事です。
type StoredArticle struct {
	ID         int64
	SourceName string
	domain.Article
	FetchedAt time.Time
}

// ArticleQuery は、管理画面の記事一覧条件を表します。
type ArticleQuery struct {
	SourceID int64
	Keyword  string
	Limit    int
	Offset   int
}

// ArticlePage は、記事一覧と全件数を表します。
type ArticlePage struct {
	Items []StoredArticle
	Total int
}

/*
ArticleRevisionExists は、取得対象内に同じ記事リビジョンが保存済みか確認します。

機能:
  - source_id、external_id、revision_id で保存中のリビジョンを確認する
  - Reuters の詳細 API を不要な再取得から保護する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - sourceID int64: 取得対象 ID
  - externalID string: 取得元の記事 ID
  - revisionID string: 取得元のリビジョン ID

返り値:
  - bool: 同じ記事リビジョンが存在する場合は true
  - error: DB 問い合わせに失敗した場合のエラー
*/
func (s *Store) ArticleRevisionExists(
	ctx context.Context,
	sourceID int64,
	externalID string,
	revisionID string,
) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM news_articles
			WHERE source_id = $1
				AND external_id = $2
				AND revision_id = $3
		)`,
		sourceID,
		externalID,
		revisionID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("記事リビジョンの存在を確認できません: %w", err)
	}
	return exists, nil
}

// ----------------------------------------

/*
InsertArticle は、同じ記事と更新日時の内容を上書き保存します。

機能:
  - 空の原データを空 JSON オブジェクトへ補正する
  - 取得対象、外部記事 ID、更新日時を一意キーとし、更新日時がなければ公開日時を使う
  - 日時が両方未設定の記事も一行にまとめ、内容が変わった場合だけ上書きする
  - 記事 ID と初回の取得日時を維持し、日時が異なる場合だけ履歴を追加する
  - 保持期限が有効な場合だけ、更新日時、公開日時の順で判定して 168 時間以上前の記事は保存しない
  - RETURNING を使って新規保存と上書き・重複を区別する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - article domain.Article: 保存する見出しと本文を含む記事

返り値:
  - bool: 新規保存した場合は true、上書き・変更なし・保存期限切れの場合は false
  - error: INSERT に失敗した場合のエラー
*/
func (s *Store) InsertArticle(ctx context.Context, article domain.Article) (bool, error) {
	rawPayload := article.RawPayload
	if len(rawPayload) == 0 {
		rawPayload = json.RawMessage(`{}`)
	}

	var inserted bool
	err := s.db.QueryRowContext(
		ctx,
		`INSERT INTO news_articles (
			source_id,
			provider,
			external_id,
			revision_id,
			canonical_url,
			published_at,
			updated_at,
			headline,
			body_text,
			raw_payload
		) SELECT
			$1::BIGINT, $2::TEXT, $3::TEXT, $4::TEXT, $5::TEXT,
			$6::TIMESTAMPTZ, $7::TIMESTAMPTZ, $8::TEXT, $9::TEXT, $10::JSONB
		WHERE NOT $11::BOOLEAN
			OR COALESCE($7::TIMESTAMPTZ, $6::TIMESTAMPTZ, CURRENT_TIMESTAMP)
				> CURRENT_TIMESTAMP - INTERVAL '168 hours'
		ON CONFLICT (
			source_id, external_id,
			(COALESCE(updated_at, published_at, '-infinity'::TIMESTAMPTZ))
		) DO UPDATE SET
			provider = EXCLUDED.provider,
			revision_id = EXCLUDED.revision_id,
			canonical_url = EXCLUDED.canonical_url,
			published_at = EXCLUDED.published_at,
			updated_at = EXCLUDED.updated_at,
			headline = EXCLUDED.headline,
			body_text = EXCLUDED.body_text,
			raw_payload = EXCLUDED.raw_payload
		WHERE (
			NOT $11::BOOLEAN
			OR COALESCE(news_articles.updated_at, news_articles.published_at, news_articles.fetched_at)
				> CURRENT_TIMESTAMP - INTERVAL '168 hours'
		)
		AND (
			news_articles.provider, news_articles.revision_id, news_articles.canonical_url,
			news_articles.published_at, news_articles.updated_at, news_articles.headline,
			news_articles.body_text, news_articles.raw_payload
		) IS DISTINCT FROM (
			EXCLUDED.provider, EXCLUDED.revision_id, EXCLUDED.canonical_url,
			EXCLUDED.published_at, EXCLUDED.updated_at, EXCLUDED.headline,
			EXCLUDED.body_text, EXCLUDED.raw_payload
		)
		RETURNING (xmax = 0) AS inserted`,
		article.SourceID,
		article.Provider,
		article.ExternalID,
		article.RevisionID,
		article.CanonicalURL,
		article.PublishedAt,
		article.UpdatedAt,
		article.Headline,
		article.BodyText,
		string(rawPayload),
		s.articleRetentionEnabled,
	).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"記事を保存できません: source_id=%d external_id=%q: %w",
			article.SourceID,
			article.ExternalID,
			err,
		)
	}
	return inserted, nil
}

// ----------------------------------------

/*
ListArticles は、管理画面用の記事ページを検索します。

機能:
  - 取得対象とキーワードによる絞り込みを行う
  - 公開日時と ID の降順でページングする
  - 同じ条件の全件数も返す

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - query ArticleQuery: 絞り込みとページング条件

返り値:
  - ArticlePage: 保存済み記事と全件数
  - error: 件数または一覧の問い合わせに失敗した場合のエラー
*/
func (s *Store) ListArticles(ctx context.Context, query ArticleQuery) (ArticlePage, error) {
	keyword := strings.TrimSpace(query.Keyword)
	if query.Limit < 1 {
		query.Limit = 50
	}
	if query.Limit > 200 {
		query.Limit = 200
	}
	if query.Offset < 0 {
		query.Offset = 0
	}

	const whereClause = `
		WHERE ($1::BIGINT = 0 OR a.source_id = $1)
			AND (
				$2::TEXT = ''
				OR a.headline ILIKE '%' || $2 || '%'
				OR a.body_text ILIKE '%' || $2 || '%'
			)`

	var total int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM news_articles a`+whereClause,
		query.SourceID,
		keyword,
	).Scan(&total); err != nil {
		return ArticlePage{}, fmt.Errorf("記事件数を取得できません: %w", err)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT
			a.id,
			s.name,
			a.source_id,
			a.provider,
			a.external_id,
			a.revision_id,
			a.canonical_url,
			a.published_at,
			a.updated_at,
			a.headline,
			LEFT(a.body_text, 500),
			a.fetched_at
		FROM news_articles a
		INNER JOIN news_sources s ON s.id = a.source_id`+whereClause+`
		ORDER BY a.published_at DESC NULLS LAST, a.id DESC
		LIMIT $3 OFFSET $4`,
		query.SourceID,
		keyword,
		query.Limit,
		query.Offset,
	)
	if err != nil {
		return ArticlePage{}, fmt.Errorf("記事を一覧できません: %w", err)
	}
	defer rows.Close()

	items := make([]StoredArticle, 0, query.Limit)
	for rows.Next() {
		article, scanErr := scanStoredArticleSummary(rows)
		if scanErr != nil {
			return ArticlePage{}, scanErr
		}
		items = append(items, article)
	}
	if err := rows.Err(); err != nil {
		return ArticlePage{}, fmt.Errorf("記事一覧を読み込めません: %w", err)
	}
	return ArticlePage{Items: items, Total: total}, nil
}

// ----------------------------------------

/*
scanStoredArticleSummary は、一覧用に本文抜粋だけを持つ保存済み記事へ変換します。

機能:
  - 記事の基本列と DB 側で短縮した本文を読み込む
  - 一覧で不要な raw_payload をメモリへ読み込まない

引数:
  - scanner rowScanner: sql.Rows の現在行

返り値:
  - StoredArticle: 一覧表示に必要な保存済み記事
  - error: 列の読込に失敗した場合のエラー
*/
func scanStoredArticleSummary(scanner rowScanner) (StoredArticle, error) {
	var article StoredArticle
	err := scanner.Scan(
		&article.ID,
		&article.SourceName,
		&article.SourceID,
		&article.Provider,
		&article.ExternalID,
		&article.RevisionID,
		&article.CanonicalURL,
		&article.PublishedAt,
		&article.UpdatedAt,
		&article.Headline,
		&article.BodyText,
		&article.FetchedAt,
	)
	if err != nil {
		return StoredArticle{}, fmt.Errorf("記事一覧行を読み込めません: %w", err)
	}
	return article, nil
}

// ----------------------------------------

/*
GetArticle は、指定 ID の保存済み記事を返します。

機能:
  - 取得対象名を結合して記事詳細を読み込む

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - id int64: 記事 ID

返り値:
  - StoredArticle: 指定された保存済み記事
  - error: 対象がない場合または DB 問い合わせに失敗した場合のエラー
*/
func (s *Store) GetArticle(ctx context.Context, id int64) (StoredArticle, error) {
	article, err := scanStoredArticle(s.db.QueryRowContext(
		ctx,
		`SELECT
			a.id,
			s.name,
			a.source_id,
			a.provider,
			a.external_id,
			a.revision_id,
			a.canonical_url,
			a.published_at,
			a.updated_at,
			a.headline,
			a.body_text,
			a.fetched_at
		FROM news_articles a
		INNER JOIN news_sources s ON s.id = a.source_id
		WHERE a.id = $1`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return StoredArticle{}, ErrNotFound
	}
	if err != nil {
		return StoredArticle{}, err
	}
	return article, nil
}

// ----------------------------------------

/*
scanStoredArticle は、DB 行を管理画面用の保存済み記事へ変換します。

機能:
  - 記事列と取得対象名を StoredArticle へ読み込む

引数:
  - scanner rowScanner: sql.Row または sql.Rows

返り値:
  - StoredArticle: 読み込んだ保存済み記事
  - error: 列の読込に失敗した場合のエラー
*/
func scanStoredArticle(scanner rowScanner) (StoredArticle, error) {
	var article StoredArticle
	err := scanner.Scan(
		&article.ID,
		&article.SourceName,
		&article.SourceID,
		&article.Provider,
		&article.ExternalID,
		&article.RevisionID,
		&article.CanonicalURL,
		&article.PublishedAt,
		&article.UpdatedAt,
		&article.Headline,
		&article.BodyText,
		&article.FetchedAt,
	)
	if err != nil {
		return StoredArticle{}, fmt.Errorf("記事を読み込めません: %w", err)
	}
	return article, nil
}
