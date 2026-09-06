package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/masayoshi4649/NewsStore/internal/config"
	"github.com/masayoshi4649/NewsStore/internal/domain"
)

/*
TestLiveArticleTimestampIdentity は、実 PostgreSQL で記事の日時による一意性を検証します。

機能:
  - 明示的な環境変数指定時だけ独立した一時スキーマを使用する
  - 上書き、変更なし、異なる日時の履歴、日時未設定、同時保存を検証する
  - 同じ見出しでも取得対象または外部記事 ID が異なる記事を保持する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveArticleTimestampIdentity(t *testing.T) {
	store := newArticleIdentityLiveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("一時スキーマの移行に失敗しました: %v", err)
	}
	sourceID := insertArticleIdentityLiveSource(t, ctx, store, "取得対象1")
	otherSourceID := insertArticleIdentityLiveSource(t, ctx, store, "取得対象2")

	t.Run("同日時の上書きと変更なし", func(t *testing.T) {
		article := testArticle()
		article.SourceID = sourceID
		article.ExternalID = "overwrite"
		assertArticleIdentityLiveInsert(t, ctx, store, article, true)
		before := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)

		article.RevisionID = "revision-2"
		article.Provider = "reuters"
		article.CanonicalURL = "https://example.com/changed"
		publishedAt := article.PublishedAt.Add(-time.Hour)
		article.PublishedAt = &publishedAt
		article.Headline = "差し替え後の見出し"
		article.BodyText = "差し替え後の本文"
		article.RawPayload = json.RawMessage(`{"変更":true}`)
		assertArticleIdentityLiveInsert(t, ctx, store, article, false)
		after := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
		if after.id != before.id || !after.fetchedAt.Equal(before.fetchedAt) {
			t.Errorf("上書きで記事 ID または初回取得日時が変化しました: 前=%+v 後=%+v", before, after)
		}
		stored, err := store.GetArticle(ctx, before.id)
		if err != nil {
			t.Fatalf("上書き後の記事を読み込めません: %v", err)
		}
		if stored.RevisionID != article.RevisionID || stored.Provider != article.Provider ||
			stored.CanonicalURL != article.CanonicalURL || stored.Headline != article.Headline ||
			stored.BodyText != article.BodyText || !stored.PublishedAt.Equal(publishedAt) {
			t.Errorf("記事内容が上書きされていません: %+v", stored)
		}
		var rawMatches bool
		if err := store.db.QueryRowContext(ctx,
			`SELECT raw_payload = $2::JSONB FROM news_articles WHERE id = $1`,
			before.id, string(article.RawPayload)).Scan(&rawMatches); err != nil || !rawMatches {
			t.Fatalf("原データが上書きされていません: 一致=%v エラー=%v", rawMatches, err)
		}

		// ----------------------------------------

		assertArticleIdentityLiveInsert(t, ctx, store, article, false)
		unchanged := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
		if unchanged != after {
			t.Errorf("同一内容の再取得で行が更新されました: 前=%+v 後=%+v", after, unchanged)
		}
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, article.ExternalID, 1)

		// ----------------------------------------

		updatedAt := article.UpdatedAt.Add(time.Minute)
		article.UpdatedAt = &updatedAt
		assertArticleIdentityLiveInsert(t, ctx, store, article, true)
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, article.ExternalID, 2)
	})

	// ----------------------------------------

	t.Run("日時未設定時の代替値", func(t *testing.T) {
		for _, noDates := range []bool{false, true} {
			article := testArticle()
			article.SourceID = sourceID
			article.ExternalID = fmt.Sprintf("fallback-%t", noDates)
			article.UpdatedAt = nil
			if noDates {
				article.PublishedAt = nil
			}
			assertArticleIdentityLiveInsert(t, ctx, store, article, true)
			before := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
			article.RevisionID = "revision-2"
			article.BodyText = "変更された本文"
			assertArticleIdentityLiveInsert(t, ctx, store, article, false)
			after := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
			if before.id != after.id || !before.fetchedAt.Equal(after.fetchedAt) {
				t.Errorf("日時の代替値を使った上書きで ID または初回取得日時が変わりました")
			}
			assertArticleIdentityLiveCount(t, ctx, store, sourceID, article.ExternalID, 1)
			if !noDates {
				publishedAt := article.PublishedAt.Add(time.Minute)
				article.PublishedAt = &publishedAt
				assertArticleIdentityLiveInsert(t, ctx, store, article, true)
				assertArticleIdentityLiveCount(t, ctx, store, sourceID, article.ExternalID, 2)
			}
		}
	})

	// ----------------------------------------

	t.Run("同時取得でも一行だけ保存", func(t *testing.T) {
		article := testArticle()
		article.SourceID = sourceID
		article.ExternalID = "concurrent"
		type result struct {
			inserted bool
			err      error
		}
		const workers = 8
		results := make(chan result, workers)
		start := make(chan struct{})
		var ready sync.WaitGroup
		ready.Add(workers)
		for index := range workers {
			go func(index int) {
				candidate := article
				candidate.RevisionID = fmt.Sprintf("revision-%d", index)
				ready.Done()
				<-start
				inserted, err := store.InsertArticle(ctx, candidate)
				results <- result{inserted: inserted, err: err}
			}(index)
		}
		ready.Wait()
		close(start)
		insertedCount := 0
		for range workers {
			result := <-results
			if result.err != nil {
				t.Errorf("同時保存に失敗しました: %v", result.err)
			}
			if result.inserted {
				insertedCount++
			}
		}
		if insertedCount != 1 {
			t.Errorf("同時保存の新規保存件数 = %d、期待値 = 1", insertedCount)
		}
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, article.ExternalID, 1)
	})

	// ----------------------------------------

	t.Run("別の取得対象と記事を保持", func(t *testing.T) {
		article := testArticle()
		article.SourceID = sourceID
		article.ExternalID = "independent"
		assertArticleIdentityLiveInsert(t, ctx, store, article, true)
		article.SourceID = otherSourceID
		assertArticleIdentityLiveInsert(t, ctx, store, article, true)
		article.SourceID = sourceID
		article.ExternalID = "independent-other"
		assertArticleIdentityLiveInsert(t, ctx, store, article, true)
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, "independent", 1)
		assertArticleIdentityLiveCount(t, ctx, store, otherSourceID, "independent", 1)
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, "independent-other", 1)
	})

	// ----------------------------------------

	t.Run("保持期限が無効なら過去記事を保存して上書きできる", func(t *testing.T) {
		oldDate := time.Now().UTC().Truncate(time.Microsecond).Add(-30 * 24 * time.Hour)
		for _, test := range []struct {
			external  string
			published *time.Time
			updated   *time.Time
		}{
			{"保持期限無効の更新日時", &oldDate, &oldDate},
			{"保持期限無効の公開日時", &oldDate, nil},
			{"保持期限無効の取得日時", nil, nil},
		} {
			article := testArticle()
			article.SourceID = sourceID
			article.ExternalID = test.external
			article.PublishedAt = test.published
			article.UpdatedAt = test.updated
			assertArticleIdentityLiveInsert(t, ctx, store, article, true)
			if _, err := store.db.ExecContext(ctx,
				`UPDATE news_articles SET fetched_at = $3 WHERE source_id = $1 AND external_id = $2`,
				sourceID, article.ExternalID, oldDate); err != nil {
				t.Fatalf("過去記事の取得日時を設定できません: %v", err)
			}
			before := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
			article.RevisionID = "過去記事の更新リビジョン"
			article.BodyText = "上書き後の過去記事本文"
			assertArticleIdentityLiveInsert(t, ctx, store, article, false)
			after := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
			stored, err := store.GetArticle(ctx, before.id)
			if err != nil {
				t.Fatalf("上書き後の過去記事を読み込めません: %v", err)
			}
			if stored.RevisionID != article.RevisionID || stored.BodyText != article.BodyText {
				t.Errorf("保持期限が無効なのに過去記事が上書きされません: %+v", stored)
			}
			if before.id != after.id || !before.fetchedAt.Equal(after.fetchedAt) {
				t.Errorf("過去記事の上書きで ID または初回取得日時が変化しました")
			}
			assertArticleIdentityLiveCount(t, ctx, store, sourceID, article.ExternalID, 1)
		}
	})
}

// ----------------------------------------

/*
TestLiveArticleTimestampMigration は、既存データの重複整理を実 PostgreSQL で検証します。

機能:
  - 旧スキーマに同日時の重複を作り、通常の移行で最新取得行だけ残ることを確認する
  - 取得日時が同じ場合は最大 ID の行を保持し、異なる記事と日時の履歴を保つ

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveArticleTimestampMigration(t *testing.T) {
	store := newArticleIdentityLiveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sourceID := insertArticleIdentityLiveSource(t, ctx, store, "移行対象1")
	otherSourceID := insertArticleIdentityLiveSource(t, ctx, store, "移行対象2")
	now := time.Now().UTC().Truncate(time.Microsecond)
	publishedAt := now.Add(-3 * time.Hour)
	updatedAt := now.Add(-2 * time.Hour)
	otherUpdatedAt := updatedAt.Add(-time.Minute)
	type fixture struct {
		sourceID  int64
		external  string
		revision  string
		published *time.Time
		updated   *time.Time
		fetched   time.Time
		keep      bool
	}
	fixtures := []fixture{
		{sourceID, "same", "latest-first", &publishedAt, &updatedAt, now, false},
		{sourceID, "same", "latest-tie", &publishedAt, &updatedAt, now, true},
		{sourceID, "same", "older-last", &publishedAt, &updatedAt, now.Add(-time.Hour), false},
		{sourceID, "published", "old", &publishedAt, nil, now.Add(-time.Hour), false},
		{sourceID, "published", "latest", &publishedAt, nil, now, true},
		{sourceID, "no-dates", "old", nil, nil, now.Add(-time.Hour), false},
		{sourceID, "no-dates", "latest", nil, nil, now, true},
		{otherSourceID, "same", "other-source", &publishedAt, &updatedAt, now, true},
		{sourceID, "other-article", "other-article", &publishedAt, &updatedAt, now, true},
		{sourceID, "same", "history", &publishedAt, &otherUpdatedAt, now, true},
	}
	expected := make(map[int64]string)
	for _, fixture := range fixtures {
		var id int64
		err := store.db.QueryRowContext(ctx, `INSERT INTO news_articles (
			source_id, provider, external_id, revision_id, published_at, updated_at,
			headline, body_text, fetched_at
		) VALUES ($1, 'rss', $2, $3, $4, $5, '同じ見出し', '同じ本文', $6) RETURNING id`,
			fixture.sourceID, fixture.external, fixture.revision, fixture.published,
			fixture.updated, fixture.fetched).Scan(&id)
		if err != nil {
			t.Fatalf("旧スキーマへ検証用の記事を保存できません: %v", err)
		}
		if fixture.keep {
			expected[id] = fixture.revision
		}
	}

	// ----------------------------------------

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("重複データを移行できません: %v", err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, revision_id FROM news_articles`)
	if err != nil {
		t.Fatalf("移行結果を取得できません: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var revision string
		if err := rows.Scan(&id, &revision); err != nil {
			t.Fatalf("移行結果を読み込めません: %v", err)
		}
		if expected[id] != revision {
			t.Errorf("削除対象の記事または変更された内容が残っています: ID=%d リビジョン=%q", id, revision)
		}
		delete(expected, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("移行結果の走査に失敗しました: %v", err)
	}
	if len(expected) != 0 {
		t.Errorf("残すべき記事が削除されています: %v", expected)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("移行結果を閉じられません: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("適用済み移行の再実行に失敗しました: %v", err)
	}
}

// ----------------------------------------

/*
TestLiveArticleRetention は、実 PostgreSQL で保存期限の境界と日時の優先順位を検証します。

機能:
  - 専用の単一接続とトランザクションで現在時刻を固定し、168 時間の境界を確認する
  - 期限切れの再保存を拒否し、更新日時が新しい記事を保持する
  - 日時未設定の記事は上書き後も初回取得日時から期限を判定する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveArticleRetention(t *testing.T) {
	store := newArticleIdentityLiveStore(t)
	store.SetArticleRetentionEnabled(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("期限検証用のスキーマを移行できません: %v", err)
	}
	sourceID := insertArticleIdentityLiveSource(t, ctx, store, "期限検証")
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	if _, err := store.db.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("期限検証の現在時刻を固定できません: %v", err)
	}
	t.Cleanup(func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rollbackCancel()
		if _, err := store.db.ExecContext(rollbackCtx, "ROLLBACK"); err != nil {
			t.Errorf("期限検証のトランザクションを終了できません: %v", err)
		}
	})
	var now, cutoff time.Time
	if err := store.db.QueryRowContext(ctx,
		`SELECT CURRENT_TIMESTAMP, CURRENT_TIMESTAMP - INTERVAL '168 hours'`).Scan(&now, &cutoff); err != nil {
		t.Fatalf("期限判定の基準時刻を取得できません: %v", err)
	}
	older := cutoff.Add(-time.Microsecond)
	newer := cutoff.Add(time.Microsecond)
	recent := now.Add(-time.Hour)

	// ----------------------------------------

	for _, test := range []struct {
		name      string
		published *time.Time
		updated   *time.Time
		inserted  bool
	}{
		{"期限超過", &now, &older, false},
		{"更新日時がちょうど168時間前", &now, &cutoff, false},
		{"更新日時が境界の1マイクロ秒後", &older, &newer, true},
		{"古い公開日時と新しい更新日時", &older, &recent, true},
		{"更新日時なしで公開日時が期限超過", &older, nil, false},
		{"公開日時がちょうど168時間前", &cutoff, nil, false},
		{"公開日時が境界の1マイクロ秒後", &newer, nil, true},
	} {
		article := testArticle()
		article.SourceID = sourceID
		article.ExternalID = test.name
		article.PublishedAt = test.published
		article.UpdatedAt = test.updated
		assertArticleIdentityLiveInsert(t, ctx, store, article, test.inserted)
		wantCount := 0
		if test.inserted {
			wantCount = 1
		}
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, article.ExternalID, wantCount)
	}

	// ----------------------------------------

	article := testArticle()
	article.SourceID = sourceID
	article.ExternalID = "日時なしの初回取得期限"
	article.PublishedAt = nil
	article.UpdatedAt = nil
	assertArticleIdentityLiveInsert(t, ctx, store, article, true)
	if _, err := store.db.ExecContext(ctx,
		`UPDATE news_articles SET fetched_at = $3 WHERE source_id = $1 AND external_id = $2`,
		sourceID, article.ExternalID, cutoff); err != nil {
		t.Fatalf("日時なしの記事の初回取得日時を設定できません: %v", err)
	}
	before := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
	article.RevisionID = "再取得リビジョン"
	assertArticleIdentityLiveInsert(t, ctx, store, article, false)
	after := readArticleIdentityLiveState(t, ctx, store, sourceID, article.ExternalID)
	if before.id != after.id || !after.fetchedAt.Equal(cutoff) {
		t.Errorf("日時なしの記事の再取得で保存期限が延長されました: %+v", after)
	}
	for _, row := range []struct {
		external  string
		published *time.Time
		updated   *time.Time
	}{
		{"削除用の更新日時境界", &now, &cutoff},
		{"削除用の公開日時境界", &cutoff, nil},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO news_articles (
			source_id, provider, external_id, revision_id, published_at, updated_at, headline, body_text
		) VALUES ($1, 'rss', $2, 'revision', $3, $4, '期限検証', '期限検証の本文')`,
			sourceID, row.external, row.published, row.updated); err != nil {
			t.Fatalf("削除検証用の過去記事を保存できません: %v", err)
		}
	}

	// ----------------------------------------

	deleted, err := store.DeleteExpiredArticles(ctx)
	if err != nil {
		t.Fatalf("保存期限を過ぎた記事を削除できません: %v", err)
	}
	if deleted != 3 {
		t.Errorf("保存期限による削除件数 = %d、期待値 = 3", deleted)
	}
	for _, external := range []string{
		"日時なしの初回取得期限", "削除用の更新日時境界", "削除用の公開日時境界",
	} {
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, external, 0)
	}
	for _, external := range []string{
		"更新日時が境界の1マイクロ秒後", "古い公開日時と新しい更新日時", "公開日時が境界の1マイクロ秒後",
	} {
		assertArticleIdentityLiveCount(t, ctx, store, sourceID, external, 1)
	}
	deleted, err = store.DeleteExpiredArticles(ctx)
	if err != nil || deleted != 0 {
		t.Errorf("期限削除の再実行で変更が発生しました: 件数=%d エラー=%v", deleted, err)
	}
}

// ----------------------------------------

/*
newArticleIdentityLiveStore は、共有テーブルへ到達しない実 DB テスト用 Store を作ります。

機能:
  - NEWSSTORE_TEST_POSTGRES_WRITE_LIVE=1 の場合だけ設定を読み込み接続する
  - 無作為なスキーマだけを search_path に設定した別接続を作る
  - 旧マイグレーションを準備し、終了時に自分が作成したスキーマだけを削除する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - *Store: 独立した一時スキーマだけを使用する Store
*/
func newArticleIdentityLiveStore(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("NEWSSTORE_TEST_POSTGRES_WRITE_LIVE") != "1" {
		t.Skip("独立スキーマの実 DB テストは NEWSSTORE_TEST_POSTGRES_WRITE_LIVE=1 の場合だけ実行します")
	}
	configDirectory := os.Getenv("NEWSSTORE_TEST_POSTGRES_CONF")
	if configDirectory == "" {
		configDirectory = filepath.Join("..", "..", "..", "conf")
	}
	cfg, err := config.LoadDir(configDirectory)
	if err != nil {
		t.Fatalf("実 DB テスト設定を読み込めません: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("実 DB へ接続できません: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var randomBytes [12]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("一時スキーマ名を生成できません: %v", err)
	}
	schema := "newsstore_article_test_" + hex.EncodeToString(randomBytes[:])
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.db.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("独立した一時スキーマを作成できません: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.db.ExecContext(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("テスト専用スキーマ %q を削除できません: %v", schema, err)
		}
	})

	// ----------------------------------------

	connectionURL, err := url.Parse(buildConnectionURL(cfg.Postgres))
	if err != nil {
		t.Fatal("実 DB 接続 URL を解釈できません")
	}
	query := connectionURL.Query()
	query.Set("search_path", schema)
	connectionURL.RawQuery = query.Encode()
	db, err := sql.Open("pgx", connectionURL.String())
	if err != nil {
		t.Fatalf("一時スキーマ用の接続を初期化できません: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)
	var currentSchema, searchPath string
	if err := db.QueryRowContext(ctx, `SELECT CURRENT_SCHEMA(), CURRENT_SETTING('search_path')`).
		Scan(&currentSchema, &searchPath); err != nil {
		t.Fatalf("一時スキーマへの接続を確認できません: %v", err)
	}
	if currentSchema != schema || searchPath != schema {
		t.Fatalf("接続先がテスト専用スキーマに限定されていません: 現在=%q 検索先=%q", currentSchema, searchPath)
	}
	store := New(db)
	prepareArticleIdentityLegacySchema(t, ctx, store)
	return store
}

// ----------------------------------------

/*
prepareArticleIdentityLegacySchema は、隔離されたスキーマに 004 までの旧構成を準備します。

機能:
  - 元の移行 SQL とチェックサムを使って旧スキーマを構築する
  - pg_trgm が共有スキーマにある場合も演算子クラスだけを明示的に参照する
  - 共有スキーマを search_path に追加せず、共有テーブルへ書き込まない

引数:
  - t *testing.T: テスト状態を管理する値
  - ctx context.Context: DB 操作の期限を伝えるコンテキスト
  - store *Store: テスト専用スキーマへ接続した Store

返り値:
  - なし
*/
func prepareArticleIdentityLegacySchema(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		name TEXT PRIMARY KEY, checksum TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("一時スキーマの移行履歴を作成できません: %v", err)
	}
	for _, name := range []string{
		"001_initial.sql", "002_article_search_trigram.sql",
		"003_mcp_article_order.sql", "004_source_display_order.sql",
	} {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("旧移行ファイルを読み込めません: %v", err)
		}
		sqlBody := string(body)
		if name == "002_article_search_trigram.sql" {
			var extensionSchema string
			err := store.db.QueryRowContext(ctx, `SELECT namespace.nspname
				FROM pg_catalog.pg_extension extension
				JOIN pg_catalog.pg_namespace namespace ON namespace.oid = extension.extnamespace
				WHERE extension.extname = 'pg_trgm'`).Scan(&extensionSchema)
			if err != nil {
				t.Fatalf("既存の pg_trgm を確認できません。実 DB テスト前に拡張を準備してください: %v", err)
			}
			sqlBody = strings.ReplaceAll(sqlBody, "CREATE EXTENSION IF NOT EXISTS pg_trgm;", "")
			sqlBody = strings.ReplaceAll(sqlBody, "gin_trgm_ops", pgx.Identifier{extensionSchema, "gin_trgm_ops"}.Sanitize())
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("旧スキーマ作成のトランザクションを開始できません: %v", err)
		}
		if _, err := tx.ExecContext(ctx, sqlBody); err != nil {
			_ = tx.Rollback()
			t.Fatalf("旧スキーマ %q を準備できません: %v", name, err)
		}
		digest := sha256.Sum256(body)
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)`,
			name, hex.EncodeToString(digest[:])); err != nil {
			_ = tx.Rollback()
			t.Fatalf("旧スキーマの移行履歴を記録できません: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("旧スキーマの作成を確定できません: %v", err)
		}
	}
}

// ----------------------------------------

/*
insertArticleIdentityLiveSource は、実 DB テスト用の取得対象を保存します。

機能:
  - 一時スキーマへ取得対象を新規作成する

引数:
  - t *testing.T: テスト状態を管理する値
  - ctx context.Context: DB 操作の期限を伝えるコンテキスト
  - store *Store: テスト用 Store
  - name string: 取得対象名

返り値:
  - int64: 作成した取得対象の ID
*/
func insertArticleIdentityLiveSource(t *testing.T, ctx context.Context, store *Store, name string) int64 {
	t.Helper()
	var id int64
	if err := store.db.QueryRowContext(ctx, `INSERT INTO news_sources (name, kind, endpoint_url)
		VALUES ($1, 'rss', 'https://example.com/feed') RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("テスト用の取得対象を作成できません: %v", err)
	}
	return id
}

// ----------------------------------------

/*
assertArticleIdentityLiveInsert は、実際の記事保存と新規保存の返り値を確認します。

機能:
  - 実 DB に記事を保存し、期待する新規保存判定と比較する

引数:
  - t *testing.T: テスト状態を管理する値
  - ctx context.Context: DB 操作の期限を伝えるコンテキスト
  - store *Store: テスト用 Store
  - article domain.Article: 保存する記事
  - wantInserted bool: 期待する新規保存判定

返り値:
  - なし
*/
func assertArticleIdentityLiveInsert(t *testing.T, ctx context.Context, store *Store, article domain.Article, wantInserted bool) {
	t.Helper()
	inserted, err := store.InsertArticle(ctx, article)
	if err != nil {
		t.Fatalf("記事 %q を保存できません: %v", article.ExternalID, err)
	}
	if inserted != wantInserted {
		t.Errorf("記事 %q の新規保存判定 = %v、期待値 = %v", article.ExternalID, inserted, wantInserted)
	}
}

// ----------------------------------------

/*
assertArticleIdentityLiveCount は、取得対象と外部記事 ID ごとの保存件数を確認します。

機能:
  - 一時スキーマの行数を読み込み、履歴が過不足なく保存されたことを検証する

引数:
  - t *testing.T: テスト状態を管理する値
  - ctx context.Context: DB 操作の期限を伝えるコンテキスト
  - store *Store: テスト用 Store
  - sourceID int64: 取得対象 ID
  - externalID string: 外部記事 ID
  - want int: 期待する保存行数

返り値:
  - なし
*/
func assertArticleIdentityLiveCount(t *testing.T, ctx context.Context, store *Store, sourceID int64, externalID string, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM news_articles WHERE source_id = $1 AND external_id = $2`,
		sourceID, externalID).Scan(&count); err != nil {
		t.Fatalf("記事件数を取得できません: %v", err)
	}
	if count != want {
		t.Errorf("取得対象 %d・記事 %q の件数 = %d、期待値 = %d", sourceID, externalID, count, want)
	}
}

// articleIdentityLiveState は、記事の識別子、初回取得日時、行更新のトランザクションを表します。
type articleIdentityLiveState struct {
	id        int64
	fetchedAt time.Time
	xmin      string
}

// ----------------------------------------

/*
readArticleIdentityLiveState は、同一記事の上書きと無変更を判別する行情報を取得します。

機能:
  - 初回取得日時と ID の維持、および無変更時の行バージョンの維持を検証できる形で返す

引数:
  - t *testing.T: テスト状態を管理する値
  - ctx context.Context: DB 操作の期限を伝えるコンテキスト
  - store *Store: テスト用 Store
  - sourceID int64: 取得対象 ID
  - externalID string: 外部記事 ID

返り値:
  - articleIdentityLiveState: 保存行の ID、初回取得日時、行バージョン
*/
func readArticleIdentityLiveState(t *testing.T, ctx context.Context, store *Store, sourceID int64, externalID string) articleIdentityLiveState {
	t.Helper()
	var state articleIdentityLiveState
	if err := store.db.QueryRowContext(ctx,
		`SELECT id, fetched_at, xmin::TEXT FROM news_articles WHERE source_id = $1 AND external_id = $2`,
		sourceID, externalID).Scan(&state.id, &state.fetchedAt, &state.xmin); err != nil {
		t.Fatalf("記事行の状態を取得できません: %v", err)
	}
	return state
}
