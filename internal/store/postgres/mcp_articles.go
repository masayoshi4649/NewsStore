package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultMCPArticleSearchLimit = 10
	maxMCPArticleSearchLimit     = 100
	maxMCPArticleSearchKeywords  = 5
	defaultMCPArticleBodyLimit   = 8000
	maxMCPArticleBodyLimit       = 16000
)

// MCPArticleSearchQuery は、MCP 向け記事一覧の軽量な検索条件を表します。
type MCPArticleSearchQuery struct {
	SourceID      int64
	Keywords      []string
	PublishedFrom *time.Time
	PublishedTo   *time.Time
	Limit         int
	BeforeTime    *time.Time
	BeforeID      int64
}

// MCPStoredArticle は、MCP 向け一覧に実効日時を付加した保存済み記事です。
type MCPStoredArticle struct {
	StoredArticle
	EffectiveAt time.Time
}

// MCPArticlePage は、MCP 向け記事一覧と後続ページの有無を表します。
type MCPArticlePage struct {
	Items   []MCPStoredArticle
	HasMore bool
}

// MCPSource は、MCP から参照できる取得対象の軽量な設定情報です。
type MCPSource struct {
	ID      int64
	Name    string
	Kind    string
	Enabled bool
}

// MCPArticleChunk は、MCP 向けの記事情報と分割した本文を表します。
type MCPArticleChunk struct {
	StoredArticle
	BodyOffset     int
	NextBodyOffset int
	HasMore        bool
}

/*
SearchArticlesForMCP は、MCP から参照する記事一覧を軽量に検索します。

機能:
  - 取得対象、実効日時範囲、最大 5 個の検索語をすべて AND 条件で適用する
  - 各検索語は見出しまたは本文の部分一致とし、SQL ワイルドカードを無効化する
  - COUNT と OFFSET を使わず、実効日時と記事 ID による keyset pagination を行う
  - 実効日時は公開日時、公開日時がない場合は取得日時を使用する
  - 指定件数より 1 件多く問い合わせ、後続ページの有無を判定する
  - 見出しと本文は先頭 500 文字だけを取得し、その他の文字列にも上限を設ける
  - raw_payload は読み込まない
  - 件数未指定時は 10 件、過大な指定時は 100 件へ補正する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - query MCPArticleSearchQuery: AND 絞り込み条件と実効日時・記事 ID カーソル

返り値:
  - MCPArticlePage: 保存済み記事の一覧と後続ページの有無
  - error: DB 問い合わせまたは行の読込に失敗した場合のエラー
*/
func (s *Store) SearchArticlesForMCP(
	ctx context.Context,
	query MCPArticleSearchQuery,
) (MCPArticlePage, error) {
	keywords := normalizeMCPArticleSearchKeywords(query.Keywords)
	if query.Limit < 1 {
		query.Limit = defaultMCPArticleSearchLimit
	}
	if query.Limit > maxMCPArticleSearchLimit {
		query.Limit = maxMCPArticleSearchLimit
	}

	var statement strings.Builder
	statement.WriteString(`SELECT
			a.id,
			LEFT(s.name, 100),
			a.source_id,
			LEFT(a.provider, 32),
			LEFT(a.external_id, 256),
			LEFT(a.revision_id, 256),
			LEFT(a.canonical_url, 1024),
			a.published_at,
			a.updated_at,
			LEFT(a.headline, 500),
			LEFT(a.body_text, 500),
			a.fetched_at,
			COALESCE(a.published_at, a.fetched_at)
		FROM news_articles a
		INNER JOIN news_sources s ON s.id = a.source_id
		WHERE 1 = 1`)
	arguments := make([]any, 0, 7+len(keywords))

	if query.SourceID > 0 {
		arguments = append(arguments, query.SourceID)
		fmt.Fprintf(&statement, "\n\t\t\tAND a.source_id = $%d", len(arguments))
	}
	if query.PublishedFrom != nil {
		arguments = append(arguments, *query.PublishedFrom)
		fmt.Fprintf(
			&statement,
			"\n\t\t\tAND COALESCE(a.published_at, a.fetched_at) >= $%d",
			len(arguments),
		)
	}
	if query.PublishedTo != nil {
		arguments = append(arguments, *query.PublishedTo)
		fmt.Fprintf(
			&statement,
			"\n\t\t\tAND COALESCE(a.published_at, a.fetched_at) <= $%d",
			len(arguments),
		)
	}
	for _, keyword := range keywords {
		arguments = append(arguments, keyword)
		placeholder := len(arguments)
		fmt.Fprintf(
			&statement,
			`
			AND (
				a.headline ILIKE '%%' || $%d || '%%' ESCAPE E'\\'
				OR a.body_text ILIKE '%%' || $%d || '%%' ESCAPE E'\\'
			)`,
			placeholder,
			placeholder,
		)
	}
	if query.BeforeTime != nil && query.BeforeID > 0 {
		arguments = append(arguments, *query.BeforeTime, query.BeforeID)
		timePlaceholder := len(arguments) - 1
		idPlaceholder := len(arguments)
		fmt.Fprintf(
			&statement,
			`
			AND (COALESCE(a.published_at, a.fetched_at), a.id) < ($%d, $%d)`,
			timePlaceholder,
			idPlaceholder,
		)
	}

	arguments = append(arguments, query.Limit+1)
	fmt.Fprintf(
		&statement,
		`
		ORDER BY COALESCE(a.published_at, a.fetched_at) DESC, a.id DESC
		LIMIT $%d`,
		len(arguments),
	)

	rows, err := s.db.QueryContext(ctx, statement.String(), arguments...)
	if err != nil {
		return MCPArticlePage{}, fmt.Errorf("MCP向け記事を一覧できません: %w", err)
	}
	defer rows.Close()

	items := make([]MCPStoredArticle, 0, query.Limit)
	hasMore := false
	for rows.Next() {
		if len(items) == query.Limit {
			hasMore = true
			break
		}

		article, scanErr := scanMCPStoredArticleSummary(rows)
		if scanErr != nil {
			return MCPArticlePage{}, scanErr
		}
		items = append(items, article)
	}
	if err := rows.Err(); err != nil {
		return MCPArticlePage{}, fmt.Errorf("MCP向け記事一覧を読み込めません: %w", err)
	}

	return MCPArticlePage{
		Items:   items,
		HasMore: hasMore,
	}, nil
}

// ----------------------------------------

/*
normalizeMCPArticleSearchKeywords は、MCP 向けの検索語を正規化します。

機能:
  - 前後の空白を除き、空になった検索語を取り除く
  - SQL ワイルドカードをリテラルとして扱えるようエスケープする
  - 防御的に検索語を先頭 5 個までに制限する

引数:
  - values []string: 利用者が指定した検索語

返り値:
  - []string: SQL の AND 条件へ渡せる最大 5 個の検索語
*/
func normalizeMCPArticleSearchKeywords(values []string) []string {
	keywords := make([]string, 0, min(len(values), maxMCPArticleSearchKeywords))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		keywords = append(keywords, escapeMCPArticleSearchKeyword(value))
		if len(keywords) == maxMCPArticleSearchKeywords {
			break
		}
	}
	return keywords
}

// ----------------------------------------

/*
escapeMCPArticleSearchKeyword は、ILIKE の検索語をリテラル文字列へ変換します。

機能:
  - バックスラッシュ、パーセント、アンダースコアをエスケープする
  - 利用者入力がワイルドカードとして全件走査を誘発することを防ぐ

引数:
  - value string: 利用者が指定した検索語

返り値:
  - string: PostgreSQL の ILIKE へ安全に渡せる検索語
*/
func escapeMCPArticleSearchKeyword(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

// ----------------------------------------

/*
scanMCPStoredArticleSummary は、DB 行を実効日時付きの MCP 向け記事へ変換します。

機能:
  - 一覧表示用の記事列を StoredArticle へ読み込む
  - 公開日時または取得日時から求めた実効日時を読み込む
  - MCP 参照で不要な raw_payload をメモリへ読み込まない

引数:
  - scanner rowScanner: sql.Rows の現在行

返り値:
  - MCPStoredArticle: 一覧用の記事情報と実効日時
  - error: 列の読込に失敗した場合のエラー
*/
func scanMCPStoredArticleSummary(scanner rowScanner) (MCPStoredArticle, error) {
	var article MCPStoredArticle
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
		&article.EffectiveAt,
	)
	if err != nil {
		return MCPStoredArticle{}, fmt.Errorf("MCP向け記事一覧行を読み込めません: %w", err)
	}
	return article, nil
}

// ----------------------------------------

/*
ListSourcesForMCP は、MCP から参照できる取得対象設定を一覧します。

機能:
  - 論理削除されていない取得対象だけを返す
  - ID、設定名、コネクタ種別、有効状態だけを読み込む
  - 保存済み表示順と ID の昇順で安定して並べる

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト

返り値:
  - []MCPSource: MCP 向けの軽量な取得対象設定一覧
  - error: DB 問い合わせまたは行の読込に失敗した場合のエラー
*/
func (s *Store) ListSourcesForMCP(ctx context.Context) ([]MCPSource, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, kind, enabled
		FROM news_sources
		WHERE deleted_at IS NULL
		ORDER BY display_order, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("MCP向け取得対象を一覧できません: %w", err)
	}
	defer rows.Close()

	sources := make([]MCPSource, 0)
	for rows.Next() {
		var source MCPSource
		if err := rows.Scan(&source.ID, &source.Name, &source.Kind, &source.Enabled); err != nil {
			return nil, fmt.Errorf("MCP向け取得対象を読み込めません: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("MCP向け取得対象一覧を読み込めません: %w", err)
	}
	return sources, nil
}

// ----------------------------------------

/*
GetArticleChunkForMCP は、MCP から参照する記事本文を指定範囲だけ取得します。

機能:
  - PostgreSQL の SUBSTRING で必要な本文範囲と判定用の 1 文字だけを取得する
  - 記事メタデータにも返却用の文字数上限を設ける
  - Unicode コードポイント単位で本文を指定件数までに制限する
  - raw_payload と本文全体を DB から読み込まない
  - 本文件数未指定時は 8000 文字、過大な指定時は 16000 文字へ補正する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - id int64: 参照する記事 ID
  - bodyOffset int: 本文先頭から読み飛ばす Unicode 文字数
  - bodyLimit int: 取得する Unicode 文字数

返り値:
  - MCPArticleChunk: 記事情報、分割本文、後続本文の有無と次の位置
  - error: 対象がない場合または DB 問い合わせに失敗した場合のエラー
*/
func (s *Store) GetArticleChunkForMCP(
	ctx context.Context,
	id int64,
	bodyOffset int,
	bodyLimit int,
) (MCPArticleChunk, error) {
	if bodyOffset < 0 {
		bodyOffset = 0
	}
	if bodyLimit < 1 {
		bodyLimit = defaultMCPArticleBodyLimit
	}
	if bodyLimit > maxMCPArticleBodyLimit {
		bodyLimit = maxMCPArticleBodyLimit
	}

	article, err := scanMCPArticleChunk(s.db.QueryRowContext(
		ctx,
		`SELECT
			a.id,
			LEFT(s.name, 100),
			a.source_id,
			LEFT(a.provider, 32),
			LEFT(a.external_id, 1024),
			LEFT(a.revision_id, 1024),
			LEFT(a.canonical_url, 4096),
			a.published_at,
			a.updated_at,
			LEFT(a.headline, 2000),
			SUBSTRING(a.body_text FROM $2 + 1 FOR $3 + 1),
			a.fetched_at
		FROM news_articles a
		INNER JOIN news_sources s ON s.id = a.source_id
		WHERE a.id = $1`,
		id,
		bodyOffset,
		bodyLimit,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return MCPArticleChunk{}, ErrNotFound
	}
	if err != nil {
		return MCPArticleChunk{}, err
	}

	bodyRunes := []rune(article.BodyText)
	hasMore := len(bodyRunes) > bodyLimit
	if hasMore {
		bodyRunes = bodyRunes[:bodyLimit]
	}
	article.BodyText = string(bodyRunes)

	nextBodyOffset := 0
	if hasMore {
		nextBodyOffset = bodyOffset + len(bodyRunes)
	}

	return MCPArticleChunk{
		StoredArticle:  article,
		BodyOffset:     bodyOffset,
		NextBodyOffset: nextBodyOffset,
		HasMore:        hasMore,
	}, nil
}

// ----------------------------------------

/*
scanMCPArticleChunk は、DB 行を MCP 向けの本文分割前の記事へ変換します。

機能:
  - 記事の基本列と DB 側で切り出した本文を StoredArticle へ読み込む
  - MCP 参照で不要な raw_payload をメモリへ読み込まない

引数:
  - scanner rowScanner: sql.Row または sql.Rows の現在行

返り値:
  - StoredArticle: 記事情報と切り出した本文
  - error: 列の読込に失敗した場合のエラー
*/
func scanMCPArticleChunk(scanner rowScanner) (StoredArticle, error) {
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
		return StoredArticle{}, fmt.Errorf("MCP向け記事を読み込めません: %w", err)
	}
	return article, nil
}
