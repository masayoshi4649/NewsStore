package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

/*
TestRecoverStaleRunsUpdatesOnlyUnlockedSources は、実行中でない取得元だけを回復することを検証します。

機能:
  - stale な実行履歴のうち取得元ロックを獲得できた履歴だけを回復することを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestRecoverStaleRunsUpdatesOnlyUnlockedSources(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT source_id")).
		WithArgs(int64(7200)).
		WillReturnRows(sqlmock.NewRows([]string{"source_id"}).
			AddRow(int64(10)).
			AddRow(int64(20)))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE ingestion_runs")).
		WithArgs(int64(7200), int64(10)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))

	recovered, err := store.RecoverStaleRuns(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatalf("RecoverStaleRuns() error = %v", err)
	}
	if recovered != 2 {
		t.Errorf("RecoverStaleRuns() = %d, 期待値は2", recovered)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestFinishRunAndSourceCommitsTogether は、履歴と次回予定が同じトランザクションで確定することを検証します。

機能:
  - 実行履歴と取得対象の完了情報が一つのトランザクションで確定することを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestFinishRunAndSourceCommitsTogether(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)
	result := RunResult{
		Status:        RunStatusPartial,
		FetchedCount:  3,
		InsertedCount: 1,
		SkippedCount:  1,
		WarningCount:  1,
		ErrorMessage:  "警告",
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE ingestion_runs")).
		WithArgs(
			int64(10),
			result.Status,
			result.FetchedCount,
			result.InsertedCount,
			result.SkippedCount,
			result.WarningCount,
			result.ErrorMessage,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE news_sources")).
		WithArgs(int64(20), result.ErrorMessage).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := store.FinishRunAndSource(
		context.Background(),
		10,
		20,
		result,
	); err != nil {
		t.Fatalf("FinishRunAndSource() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}
