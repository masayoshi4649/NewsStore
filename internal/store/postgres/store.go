// Package postgres は、PostgreSQL を使用する NewsStore の永続化機能を提供します。
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/masayoshi4649/NewsStore/internal/config"
)

// Store は、PostgreSQL 接続を共有する Repository です。
type Store struct {
	db                      *sql.DB
	articleRetentionEnabled bool
}

// ----------------------------------------

/*
SetArticleRetentionEnabled は、記事保存時の保持期限判定を切り替えます。

機能:
  - 起動時に一度だけ設定し、有効な場合だけ 7 日以上前の記事を保存対象から外す
  - 既定値は無効とし、記事保存を開始した後には変更しない

引数:
  - enabled bool: 保持期限を有効にする場合は true

返り値:
  - なし
*/
func (s *Store) SetArticleRetentionEnabled(enabled bool) {
	s.articleRetentionEnabled = enabled
}

// ----------------------------------------

/*
Open は、設定から PostgreSQL 接続を開いて疎通確認を行います。

機能:
  - PostgreSQL の接続 URL を安全に組み立てる
  - コネクションプールの上限を設定する
  - コンテキスト付き Ping で接続可能か確認する

引数:
  - ctx context.Context: 接続確認の期限とキャンセルを伝えるコンテキスト
  - cfg config.PostgresConfig: PostgreSQL の接続設定

返り値:
  - *Store: 接続確認済みの Store
  - error: 接続初期化または疎通確認に失敗した場合のエラー
*/
func Open(ctx context.Context, cfg config.PostgresConfig) (*Store, error) {
	db, err := sql.Open("pgx", buildConnectionURL(cfg))
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL 接続を初期化できません: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("PostgreSQL へ接続できません: %w", err)
	}

	return &Store{db: db}, nil
}

// ----------------------------------------

/*
New は、既存の database/sql 接続から Store を生成します。

機能:
  - テストまたは外部で管理する DB 接続を Store へ割り当てる

引数:
  - db *sql.DB: Store が利用する DB 接続

返り値:
  - *Store: 指定接続を利用する Store
*/
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// ----------------------------------------

/*
Close は、Store が保持する PostgreSQL 接続を閉じます。

機能:
  - database/sql の接続プールを解放する

引数:
  - なし

返り値:
  - error: 接続を閉じる際に発生したエラー
*/
func (s *Store) Close() error {
	return s.db.Close()
}

// ----------------------------------------

/*
Ping は、PostgreSQL への疎通を確認します。

機能:
  - ヘルスチェック用にコンテキスト付き Ping を実行する

引数:
  - ctx context.Context: 疎通確認の期限とキャンセルを伝えるコンテキスト

返り値:
  - error: 疎通確認に失敗した場合のエラー
*/
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ----------------------------------------

/*
buildConnectionURL は、PostgreSQL 設定を URL 形式の接続文字列へ変換します。

機能:
  - 認証情報と接続先を URL エンコードする
  - SSL モードをクエリへ設定する

引数:
  - cfg config.PostgresConfig: PostgreSQL の接続設定

返り値:
  - string: pgx が解釈できる PostgreSQL 接続 URL
*/
func buildConnectionURL(cfg config.PostgresConfig) string {
	connectionURL := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.Username, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   "/" + cfg.DBName,
	}
	query := connectionURL.Query()
	query.Set("sslmode", cfg.SSLMode)
	connectionURL.RawQuery = query.Encode()

	return connectionURL.String()
}
