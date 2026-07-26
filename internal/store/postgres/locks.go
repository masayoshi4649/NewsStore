package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
)

/*
AcquireSourceLock は、取得対象単位のPostgreSQL advisory lockを試行します。

機能:
  - 取得対象IDからNewsStore専用の64ビットロックキーを生成する
  - 専用DB接続上で非待機のadvisory lockを取得する
  - ロック保持中の専用接続を解放関数まで維持する

引数:
  - ctx context.Context: 接続取得とロック試行の期限・キャンセルを伝えるコンテキスト
  - sourceID int64: 排他的に取得する対象ID

返り値:
  - func(context.Context) error: ロックを解放して専用接続を閉じる関数
  - bool: ロックを取得できた場合はtrue、他プロセスが保持中の場合はfalse
  - error: DB接続またはロック試行に失敗した場合のエラー
*/
func (s *Store) AcquireSourceLock(
	ctx context.Context,
	sourceID int64,
) (func(context.Context) error, bool, error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("取得対象ロック用DB接続を取得できません: %w", err)
	}

	key := sourceLockKey(sourceID)
	var acquired bool
	if err := connection.QueryRowContext(
		ctx,
		`SELECT pg_try_advisory_lock($1)`,
		key,
	).Scan(&acquired); err != nil {
		discardConnection(connection)
		return nil, false, fmt.Errorf("取得対象のadvisory lockを試行できません: %w", err)
	}
	if !acquired {
		_ = connection.Close()
		return nil, false, nil
	}

	release := func(releaseCtx context.Context) error {
		var released bool
		err := connection.QueryRowContext(
			releaseCtx,
			`SELECT pg_advisory_unlock($1)`,
			key,
		).Scan(&released)
		if err != nil {
			discardConnection(connection)
			return fmt.Errorf("取得対象のadvisory lockを解放できません: %w", err)
		}
		defer connection.Close()
		if !released {
			return errors.New("取得対象のadvisory lockは保持されていません")
		}
		return nil
	}
	return release, true, nil
}

// ----------------------------------------

/*
discardConnection は、状態が不明な専用DB接続をプールへ戻さず破棄します。

機能:
  - driver.ErrBadConnを返してdatabase/sqlへ物理接続の再利用禁止を通知する
  - advisory lockの解放確認に失敗した接続がプールで再利用されることを防ぐ

引数:
  - connection *sql.Conn: 破棄する専用DB接続

返り値:
  - なし
*/
func discardConnection(connection *sql.Conn) {
	if connection == nil {
		return
	}

	_ = connection.Raw(func(any) error {
		return driver.ErrBadConn
	})
	_ = connection.Close()
}

// ----------------------------------------

/*
sourceLockKey は、取得対象IDから名前空間付きadvisory lockキーを生成します。

機能:
  - 固定プレフィックスと10進IDをFNV-1aでハッシュ化する
  - 他アプリケーションのadvisory lockとの衝突確率を下げる

引数:
  - sourceID int64: 取得対象ID

返り値:
  - int64: PostgreSQL advisory lockへ渡す64ビットキー
*/
func sourceLockKey(sourceID int64) int64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte("newsstore:source:"))
	_, _ = hasher.Write([]byte(strconv.FormatInt(sourceID, 10)))
	return int64(hasher.Sum64())
}
