package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/config"
)

/*
TestLiveSchemaUsesTwoWordTableNames は、実PostgreSQLのテーブル名を確認します。

機能:
  - NEWSSTORE_TEST_POSTGRES_LIVE=1が指定された場合だけ実DBテストを実行する
  - 必須4テーブルが存在し、旧1語テーブルsources、articlesが存在しないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveSchemaUsesTwoWordTableNames(t *testing.T) {
	if os.Getenv("NEWSSTORE_TEST_POSTGRES_LIVE") != "1" {
		t.Skip("PostgreSQL実DBテストはNEWSSTORE_TEST_POSTGRES_LIVE=1の場合だけ実行します")
	}

	configDirectory := os.Getenv("NEWSSTORE_TEST_POSTGRES_CONF")
	if configDirectory == "" {
		configDirectory = filepath.Join("..", "..", "..", "conf")
	}
	cfg, err := config.LoadDir(configDirectory)
	if err != nil {
		t.Fatalf("PostgreSQLテスト設定を読み込めません: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("PostgreSQLへ接続できません: %v", err)
	}
	defer store.Close()

	rows, err := store.db.QueryContext(
		ctx,
		`SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = CURRENT_SCHEMA()
			AND table_type = 'BASE TABLE'`,
	)
	if err != nil {
		t.Fatalf("PostgreSQLテーブル一覧を取得できません: %v", err)
	}
	defer rows.Close()

	tableNames := make(map[string]bool)
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			t.Fatalf("PostgreSQLテーブル名を読み込めません: %v", err)
		}
		tableNames[tableName] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("PostgreSQLテーブル一覧を読み込めません: %v", err)
	}

	for _, tableName := range []string{
		"news_sources",
		"news_articles",
		"ingestion_runs",
		"schema_migrations",
	} {
		if !tableNames[tableName] {
			t.Errorf("必須テーブルがありません: %q", tableName)
		}
	}
	for _, tableName := range []string{"sources", "articles"} {
		if tableNames[tableName] {
			t.Errorf("旧1語テーブルが残っています: %q", tableName)
		}
	}
}
