package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

/*
TestAcquireSourceLock は、advisory lockの取得と同一接続上の解放を検証します。

機能:
  - 取得元ロックの取得から同一接続上での正常解放までを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestAcquireSourceLock(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	release, acquired, err := store.AcquireSourceLock(context.Background(), 42)
	if err != nil {
		t.Fatalf("AcquireSourceLock() error = %v", err)
	}
	if !acquired || release == nil {
		t.Fatalf(
			"AcquireSourceLock() = acquired:%t release_nil:%t, 取得成功を期待",
			acquired,
			release == nil,
		)
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))
	if err := release(context.Background()); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestAcquireSourceLockDiscardsConnectionWhenUnlockFails は、解放確認に失敗した接続を再利用しない経路を検証します。

機能:
  - advisory lock の解放失敗が呼び出し元へエラーとして返ることを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestAcquireSourceLockDiscardsConnectionWhenUnlockFails(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	release, acquired, err := store.AcquireSourceLock(context.Background(), 42)
	if err != nil {
		t.Fatalf("AcquireSourceLock() error = %v", err)
	}
	if !acquired || release == nil {
		t.Fatalf(
			"AcquireSourceLock() = acquired:%t release_nil:%t, 取得成功を期待",
			acquired,
			release == nil,
		)
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(errors.New("通信断"))
	if err := release(context.Background()); err == nil {
		t.Fatal("release() error = nil, 解放失敗を期待")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}
