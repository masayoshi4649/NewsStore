package postgres

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

/*
TestSearchArticlesForMCPAppliesAllConditions は、複数条件の AND 検索を検証します。

機能:
  - 取得対象、実効日時範囲、複数検索語をすべて SQL へ渡すことを確認する
  - 実効日時と記事 ID のカーソルを同値日時でも欠落しない形で適用する
  - 指定件数より 1 件多く取得して後続ページを判定する
  - 一覧結果へ DB が算出した実効日時を設定する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestSearchArticlesForMCPAppliesAllConditions(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	publishedFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	publishedTo := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	beforeTime := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	rows := newMCPArticleSummaryRows().
		AddRow(
			int64(100),
			"ロイター",
			int64(7),
			"reuters",
			"external-100",
			"revision-100",
			"https://example.com/100",
			beforeTime.Add(-time.Hour),
			beforeTime,
			"経済記事100",
			"本文100",
			beforeTime,
			beforeTime.Add(-time.Hour),
		).
		AddRow(
			int64(99),
			"ロイター",
			int64(7),
			"reuters",
			"external-99",
			"revision-99",
			"https://example.com/99",
			beforeTime.Add(-2*time.Hour),
			beforeTime.Add(-time.Hour),
			"経済記事99",
			"本文99",
			beforeTime,
			beforeTime.Add(-2*time.Hour),
		).
		AddRow(
			int64(98),
			"ロイター",
			int64(7),
			"reuters",
			"external-98",
			"revision-98",
			"https://example.com/98",
			nil,
			beforeTime.Add(-2*time.Hour),
			"経済記事98",
			"本文98",
			beforeTime.Add(-3*time.Hour),
			beforeTime.Add(-3*time.Hour),
		)

	mock.ExpectQuery(
		`(?s)SELECT.*COALESCE\(a\.published_at, a\.fetched_at\).*`+
			`AND a\.source_id = \$1.*`+
			`COALESCE\(a\.published_at, a\.fetched_at\) >= \$2.*`+
			`COALESCE\(a\.published_at, a\.fetched_at\) <= \$3.*`+
			`a\.headline ILIKE '%' \|\| \$4 \|\| '%'.*`+
			`a\.headline ILIKE '%' \|\| \$5 \|\| '%'.*`+
			`\(COALESCE\(a\.published_at, a\.fetched_at\), a\.id\) < \(\$6, \$7\).*`+
			`ORDER BY COALESCE\(a\.published_at, a\.fetched_at\) DESC, a\.id DESC.*LIMIT \$8`,
	).
		WithArgs(
			int64(7),
			publishedFrom,
			publishedTo,
			"経済",
			"速報",
			beforeTime,
			int64(101),
			3,
		).
		WillReturnRows(rows).
		RowsWillBeClosed()

	page, err := store.SearchArticlesForMCP(
		context.Background(),
		MCPArticleSearchQuery{
			SourceID:      7,
			Keywords:      []string{" 経済 ", "速報"},
			PublishedFrom: &publishedFrom,
			PublishedTo:   &publishedTo,
			Limit:         2,
			BeforeTime:    &beforeTime,
			BeforeID:      101,
		},
	)
	if err != nil {
		t.Fatalf("SearchArticlesForMCP() error = %v", err)
	}
	if !page.HasMore {
		t.Error("SearchArticlesForMCP() HasMore = false, 期待値は true")
	}
	if len(page.Items) != 2 {
		t.Fatalf("SearchArticlesForMCP() Items length = %d, 期待値は 2", len(page.Items))
	}
	if page.Items[0].ID != 100 || page.Items[1].ID != 99 {
		t.Errorf(
			"SearchArticlesForMCP() IDs = [%d, %d], 期待値は [100, 99]",
			page.Items[0].ID,
			page.Items[1].ID,
		)
	}
	if !page.Items[0].EffectiveAt.Equal(beforeTime.Add(-time.Hour)) {
		t.Errorf(
			"SearchArticlesForMCP() EffectiveAt = %v, 期待値は %v",
			page.Items[0].EffectiveAt,
			beforeTime.Add(-time.Hour),
		)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestSearchArticlesForMCPUsesDefaultLatestLimit は、条件未指定時の最新記事検索を検証します。

機能:
  - 検索条件がなくても問い合わせできることを確認する
  - 既定 10 件と後続判定用 1 件を合わせた LIMIT 11 を使用する
  - 全件数計算と OFFSET を使わず実効日時の降順で並べる

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestSearchArticlesForMCPUsesDefaultLatestLimit(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(
		`(?s)WHERE 1 = 1\s+ORDER BY COALESCE\(a\.published_at, a\.fetched_at\) DESC, a\.id DESC\s+LIMIT \$1`,
	).
		WithArgs(11).
		WillReturnRows(newMCPArticleSummaryRows()).
		RowsWillBeClosed()

	page, err := store.SearchArticlesForMCP(context.Background(), MCPArticleSearchQuery{})
	if err != nil {
		t.Fatalf("SearchArticlesForMCP() error = %v", err)
	}
	if page.HasMore {
		t.Error("SearchArticlesForMCP() HasMore = true, 期待値は false")
	}
	if len(page.Items) != 0 {
		t.Errorf("SearchArticlesForMCP() Items length = %d, 期待値は 0", len(page.Items))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestSearchArticlesForMCPClampsKeywordsAndLimit は、検索語数と件数の防御的補正を検証します。

機能:
  - 空の検索語を除外し、最大 5 個だけを AND 条件へ追加する
  - パーセント、アンダースコア、バックスラッシュを ILIKE 用にエスケープする
  - 過大な取得件数を 100 件へ補正し、SQL の LIMIT に 101 を渡す

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestSearchArticlesForMCPClampsKeywordsAndLimit(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(
		`(?s)ESCAPE E'\\\\'.*ESCAPE E'\\\\'.*ESCAPE E'\\\\'.*`+
			`ESCAPE E'\\\\'.*ESCAPE E'\\\\'.*`+
			`ORDER BY COALESCE\(a\.published_at, a\.fetched_at\) DESC.*LIMIT \$6`,
	).
		WithArgs(`100\%\_\\完了`, "二", "三", "四", "五", 101).
		WillReturnRows(newMCPArticleSummaryRows()).
		RowsWillBeClosed()

	_, err = store.SearchArticlesForMCP(
		context.Background(),
		MCPArticleSearchQuery{
			Keywords: []string{` 100%_\完了 `, "", "二", "三", "四", "五", "六"},
			Limit:    1000,
		},
	)
	if err != nil {
		t.Fatalf("SearchArticlesForMCP() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestListSourcesForMCPReturnsLightweightSettings は、取得対象の軽量な設定一覧を検証します。

機能:
  - 論理削除されていない設定だけを保存済み表示順で問い合わせる
  - ID、設定名、コネクタ種別、有効状態を読み込む

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestListSourcesForMCPReturnsLightweightSettings(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(
		`(?s)SELECT id, name, kind, enabled.*FROM news_sources.*` +
			`WHERE deleted_at IS NULL.*ORDER BY display_order, id`,
	).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "kind", "enabled"}).
				AddRow(int64(7), "ロイター経済", "reuters", false).
				AddRow(int64(3), "国内RSS", "rss", true),
		).
		RowsWillBeClosed()

	sources, err := store.ListSourcesForMCP(context.Background())
	if err != nil {
		t.Fatalf("ListSourcesForMCP() error = %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("ListSourcesForMCP() length = %d, 期待値は 2", len(sources))
	}
	if sources[0].ID != 7 || sources[0].Name != "ロイター経済" {
		t.Errorf("ListSourcesForMCP() first = %+v, 期待値の ID は 7", sources[0])
	}
	if sources[1].ID != 3 || sources[1].Kind != "rss" || !sources[1].Enabled {
		t.Errorf("ListSourcesForMCP() second = %+v, 有効な RSS 設定を期待", sources[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestGetArticleChunkForMCPReturnsUnicodeChunk は、日本語本文の分割取得を検証します。

機能:
  - PostgreSQL へ本文位置と取得文字数を渡すことを確認する
  - 判定用の余分な 1 文字を除き、Unicode 文字単位で本文を返すことを確認する
  - 後続本文がある場合に次の本文位置を返すことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestGetArticleChunkForMCPReturnsUnicodeChunk(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	fetchedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	mock.ExpectQuery(
		`(?s)LEFT\(a\.headline, 2000\).*`+
			`SUBSTRING\(a\.body_text FROM \$2 \+ 1 FOR \$3 \+ 1\).*WHERE a\.id = \$1`,
	).
		WithArgs(int64(55), 2, 3).
		WillReturnRows(
			newMCPArticleChunkRows().AddRow(
				int64(55),
				"ロイター",
				int64(7),
				"reuters",
				"external-55",
				"revision-55",
				"https://example.com/55",
				fetchedAt.Add(-time.Hour),
				fetchedAt,
				"日本語の記事",
				"うえおか",
				fetchedAt,
			),
		)

	chunk, err := store.GetArticleChunkForMCP(context.Background(), 55, 2, 3)
	if err != nil {
		t.Fatalf("GetArticleChunkForMCP() error = %v", err)
	}
	if chunk.BodyText != "うえお" {
		t.Errorf("GetArticleChunkForMCP() BodyText = %q, 期待値は %q", chunk.BodyText, "うえお")
	}
	if chunk.BodyOffset != 2 {
		t.Errorf("GetArticleChunkForMCP() BodyOffset = %d, 期待値は 2", chunk.BodyOffset)
	}
	if !chunk.HasMore {
		t.Error("GetArticleChunkForMCP() HasMore = false, 期待値は true")
	}
	if chunk.NextBodyOffset != 5 {
		t.Errorf(
			"GetArticleChunkForMCP() NextBodyOffset = %d, 期待値は 5",
			chunk.NextBodyOffset,
		)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestGetArticleChunkForMCPReturnsNotFound は、存在しない記事のエラー変換を検証します。

機能:
  - sql.ErrNoRows を Repository 共通の ErrNotFound へ変換することを確認する
  - 負の本文位置と未指定件数を既定値へ補正することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestGetArticleChunkForMCPReturnsNotFound(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(regexp.QuoteMeta("SUBSTRING(a.body_text FROM $2 + 1 FOR $3 + 1)")).
		WithArgs(int64(404), 0, 8000).
		WillReturnError(sql.ErrNoRows)

	_, err = store.GetArticleChunkForMCP(context.Background(), 404, -1, 0)
	if err != ErrNotFound {
		t.Fatalf("GetArticleChunkForMCP() error = %v, 期待値は ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestMCPArticleOrderMigrationDefinesEffectiveIndexes は、MCP 検索用索引を検証します。

機能:
  - 実効日時と記事 ID の最新順索引が定義されていることを確認する
  - 取得対象で絞り込む場合の複合索引が定義されていることを確認する
  - 既存マイグレーションを変更せず 003 として追加されていることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestMCPArticleOrderMigrationDefinesEffectiveIndexes(t *testing.T) {
	t.Parallel()

	body, err := migrationFiles.ReadFile("migrations/003_mcp_article_order.sql")
	if err != nil {
		t.Fatalf("003_mcp_article_order.sql を読み込めません: %v", err)
	}
	statement := string(body)
	for _, required := range []string{
		"news_articles_effective_at_idx",
		"news_articles_source_effective_at_idx",
		"(COALESCE(published_at, fetched_at)) DESC, id DESC",
		"source_id, (COALESCE(published_at, fetched_at)) DESC, id DESC",
	} {
		if !strings.Contains(statement, required) {
			t.Errorf("003_mcp_article_order.sql に %q がありません", required)
		}
	}
}

// ----------------------------------------

/*
newMCPArticleSummaryRows は、MCP 向け記事一覧テスト用の SQL 行定義を生成します。

機能:
  - MCP 向け一覧で使用する記事列と実効日時の列順を定義する

引数:
  - なし

返り値:
  - *sqlmock.Rows: テストデータを追加できる SQL 行
*/
func newMCPArticleSummaryRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"name",
		"source_id",
		"provider",
		"external_id",
		"revision_id",
		"canonical_url",
		"published_at",
		"updated_at",
		"headline",
		"body_text",
		"fetched_at",
		"effective_at",
	})
}

// ----------------------------------------

/*
newMCPArticleChunkRows は、MCP 向け本文取得テスト用の SQL 行定義を生成します。

機能:
  - MCP 向け本文分割取得で使用する記事列の順序を定義する

引数:
  - なし

返り値:
  - *sqlmock.Rows: テストデータを追加できる SQL 行
*/
func newMCPArticleChunkRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"name",
		"source_id",
		"provider",
		"external_id",
		"revision_id",
		"canonical_url",
		"published_at",
		"updated_at",
		"headline",
		"body_text",
		"fetched_at",
	})
}
