package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

/*
TestMigrateAppliesEmbeddedFiles は、未適用マイグレーションを順番に確定することを検証します。

機能:
  - 排他ロックの取得と解放を検証する
  - 各埋め込みSQLとSHA-256付き適用履歴が同じトランザクションで確定されることを検証する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestMigrateAppliesEmbeddedFiles(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectExec(regexp.QuoteMeta(
		"SELECT pg_advisory_lock(hashtext('newsstore:migrations'))",
	)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectMigrationTableInitialization(mock)

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatalf("マイグレーション一覧を取得できません: %v", err)
	}
	sort.Strings(names)
	for _, name := range names {
		body, readErr := migrationFiles.ReadFile(name)
		if readErr != nil {
			t.Fatalf("マイグレーション %q を読み込めません: %v", name, readErr)
		}
		digest := sha256.Sum256(body)
		checksum := hex.EncodeToString(digest[:])
		firstStatement := strings.TrimSpace(strings.SplitN(string(body), ";", 2)[0])

		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE name = $1")).
			WithArgs(filepath.Base(name)).
			WillReturnRows(sqlmock.NewRows([]string{"checksum"}))
		mock.ExpectExec(regexp.QuoteMeta(firstStatement)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta(
			"INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)",
		)).
			WithArgs(filepath.Base(name), checksum).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	}

	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT pg_advisory_unlock(hashtext('newsstore:migrations'))",
	)).WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestMigrateRejectsChangedAppliedFile は、適用済みSQLのチェックサム変更を拒否することを検証します。

機能:
  - 保存済みチェックサムが埋め込みSQLと異なる場合にSQLを再実行しない
  - エラー時にもトランザクションと排他ロックを解放する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestMigrateRejectsChangedAppliedFile(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectExec(regexp.QuoteMeta(
		"SELECT pg_advisory_lock(hashtext('newsstore:migrations'))",
	)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectMigrationTableInitialization(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE name = $1")).
		WithArgs("001_initial.sql").
		WillReturnRows(sqlmock.NewRows([]string{"checksum"}).AddRow("変更済み"))
	mock.ExpectRollback()
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT pg_advisory_unlock(hashtext('newsstore:migrations'))",
	)).WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))

	err = store.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "チェックサムが一致しません") {
		t.Fatalf("Migrate() error = %v, チェックサム不一致を期待", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestSchemaTableNamesUseTwoWords は、全テーブル名が2語のsnake_caseであることを検証します。

機能:
  - マイグレーション管理テーブルと埋め込みSQLのCREATE TABLEを収集する
  - テーブル名がアンダースコアで区切られた空でない2語であることを確認する
  - 想定する4テーブル以外が無断で追加または削除されていないことを確認する
  - 旧1語テーブルsources、articlesへのSQL参照が残っていないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestSchemaTableNamesUseTwoWords(t *testing.T) {
	t.Parallel()

	expectedNames := map[string]struct{}{
		"schema_migrations": {},
		"news_sources":      {},
		"news_articles":     {},
		"ingestion_runs":    {},
	}
	actualNames := map[string]struct{}{
		"schema_migrations": {},
	}
	createTablePattern := regexp.MustCompile(
		`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+([a-z][a-z0-9_]*)`,
	)
	legacyTablePattern := regexp.MustCompile(
		`(?i)(?:CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS|FROM|JOIN|INTO|UPDATE|REFERENCES|ON)\s+(?:sources|articles)\b`,
	)
	var combinedSQL strings.Builder

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatalf("マイグレーション一覧を取得できません: %v", err)
	}
	for _, name := range names {
		body, readErr := migrationFiles.ReadFile(name)
		if readErr != nil {
			t.Fatalf("マイグレーション %q を読み込めません: %v", name, readErr)
		}
		combinedSQL.Write(body)
		combinedSQL.WriteByte('\n')
		for _, match := range createTablePattern.FindAllStringSubmatch(string(body), -1) {
			actualNames[strings.ToLower(match[1])] = struct{}{}
		}
	}
	if legacyReference := legacyTablePattern.FindString(combinedSQL.String()); legacyReference != "" {
		t.Errorf("旧1語テーブルへのSQL参照が残っています: %q", legacyReference)
	}

	for name := range actualNames {
		parts := strings.Split(name, "_")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Errorf("テーブル名は2語のsnake_caseが必要です: %q", name)
		}
		if _, ok := expectedNames[name]; !ok {
			t.Errorf("想定外のテーブル名です: %q", name)
		}
	}
	for name := range expectedNames {
		if _, ok := actualNames[name]; !ok {
			t.Errorf("必要なテーブルがありません: %q", name)
		}
	}
}

// ----------------------------------------

/*
expectMigrationTableInitialization は、マイグレーション管理テーブル初期化のSQL期待値を設定します。

機能:
  - schema_migrationsテーブル作成とchecksum列確認をsqlmockへ登録する

引数:
  - mock sqlmock.Sqlmock: SQL期待値を登録するモック

返り値:
  - なし
*/
func expectMigrationTableInitialization(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE IF NOT EXISTS schema_migrations")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("ALTER TABLE schema_migrations")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}
