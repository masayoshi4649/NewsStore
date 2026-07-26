package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

/*
Migrate は、未適用の埋め込み SQL マイグレーションを順番に適用します。

機能:
  - PostgreSQL advisory lock で複数プロセスの適用を直列化する
  - schema_migrations テーブルを初期化する
  - マイグレーション名の昇順で未適用 SQL を実行する
  - 適用済み SQL の SHA-256 が変更されていないことを確認する
  - SQL と適用履歴を同一トランザクションで確定する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト

返り値:
  - error: マイグレーション一覧取得、SQL 実行、または確定に失敗した場合のエラー
*/
func (s *Store) Migrate(ctx context.Context) (returnErr error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("マイグレーション用DB接続を取得できません: %w", err)
	}
	defer connection.Close()

	if _, err := connection.ExecContext(
		ctx,
		`SELECT pg_advisory_lock(hashtext('newsstore:migrations'))`,
	); err != nil {
		discardConnection(connection)
		return fmt.Errorf("マイグレーション排他ロックを取得できません: %w", err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var released bool
		releaseErr := connection.QueryRowContext(
			releaseCtx,
			`SELECT pg_advisory_unlock(hashtext('newsstore:migrations'))`,
		).Scan(&released)
		if releaseErr != nil {
			discardConnection(connection)
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("マイグレーション排他ロックを解放できません: %w", releaseErr),
			)
			return
		}
		if !released {
			returnErr = errors.Join(
				returnErr,
				errors.New("マイグレーション排他ロックは保持されていません"),
			)
		}
	}()

	if _, err := connection.ExecContext(
		ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	); err != nil {
		return fmt.Errorf("マイグレーション管理テーブルを作成できません: %w", err)
	}
	if _, err := connection.ExecContext(
		ctx,
		`ALTER TABLE schema_migrations
		ADD COLUMN IF NOT EXISTS checksum TEXT`,
	); err != nil {
		return fmt.Errorf("マイグレーションチェックサム列を確認できません: %w", err)
	}

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("マイグレーション一覧を取得できません: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := s.applyMigration(ctx, connection, name); err != nil {
			return err
		}
	}
	return nil
}

// ----------------------------------------

/*
applyMigration は、指定した SQL ファイルが未適用の場合だけ実行します。

機能:
  - 適用済みかと SQL チェックサムをトランザクション内で確認する
  - 埋め込み SQL を実行して適用履歴を保存する

引数:
  - ctx context.Context: DB 操作の期限とキャンセルを伝えるコンテキスト
  - connection *sql.Conn: advisory lock と同じセッションの専用 DB 接続
  - name string: 埋め込みマイグレーションのパス

返り値:
  - error: SQL 読込、DB 操作、またはトランザクション確定に失敗した場合のエラー
*/
func (s *Store) applyMigration(
	ctx context.Context,
	connection *sql.Conn,
	name string,
) error {
	body, err := migrationFiles.ReadFile(name)
	if err != nil {
		return fmt.Errorf("マイグレーション %q を読み込めません: %w", name, err)
	}
	digest := sha256.Sum256(body)
	checksum := hex.EncodeToString(digest[:])

	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("マイグレーション %q のトランザクションを開始できません: %w", name, err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var storedChecksum sql.NullString
	err = tx.QueryRowContext(
		ctx,
		`SELECT checksum FROM schema_migrations WHERE name = $1`,
		filepath.Base(name),
	).Scan(&storedChecksum)
	if err == nil {
		if !storedChecksum.Valid || storedChecksum.String != checksum {
			return fmt.Errorf(
				"適用済みマイグレーション %q のチェックサムが一致しません",
				name,
			)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("マイグレーション %q の適用状態を確認できません: %w", name, err)
	}

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("マイグレーション %q を実行できません: %w", name, err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)`,
		filepath.Base(name),
		checksum,
	); err != nil {
		return fmt.Errorf("マイグレーション %q の適用履歴を保存できません: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("マイグレーション %q を確定できません: %w", name, err)
	}
	return nil
}
