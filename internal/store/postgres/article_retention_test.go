package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

/*
TestDeleteExpiredArticles は、保持期限の削除条件と実行結果を検証します。

機能:
  - 更新日時、投稿日時、取得日時の順で 168 時間以上経過した記事を削除する SQL を確認する
  - 削除件数、対象なし、実行失敗、件数取得失敗を確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestDeleteExpiredArticles(t *testing.T) {
	t.Parallel()

	expectedError := errors.New("削除処理のテストエラー")
	tests := []struct {
		name        string
		deleted     int64
		execError   error
		resultError error
	}{
		{name: "期限切れ記事を削除", deleted: 3},
		{name: "期限切れ記事なし"},
		{name: "削除失敗", execError: expectedError},
		{name: "件数取得失敗", resultError: expectedError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("SQL モックを作成できません: %v", err)
			}
			defer db.Close()

			expectation := mock.ExpectExec(`
				DELETE FROM news_articles
				WHERE COALESCE(updated_at, published_at, fetched_at)
					<= CURRENT_TIMESTAMP - INTERVAL '168 hours'
			`)
			switch {
			case test.execError != nil:
				expectation.WillReturnError(test.execError)
			case test.resultError != nil:
				expectation.WillReturnResult(sqlmock.NewErrorResult(test.resultError))
			default:
				expectation.WillReturnResult(sqlmock.NewResult(0, test.deleted))
			}

			deleted, err := New(db).DeleteExpiredArticles(context.Background())
			if deleted != test.deleted {
				t.Errorf("削除件数 = %d, 期待値は %d", deleted, test.deleted)
			}
			if test.execError != nil || test.resultError != nil {
				if !errors.Is(err, expectedError) {
					t.Errorf("削除エラー = %v, 元のエラーの保持を期待", err)
				}
			} else if err != nil {
				t.Errorf("記事削除に失敗しました: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("SQL 期待値が満たされません: %v", err)
			}
		})
	}
}

// ----------------------------------------

/*
TestDeleteExpiredArticlesHonorsCancellation は、終了要求による記事削除の中止を検証します。

機能:
  - キャンセル済みコンテキストでは SQL を実行せずキャンセルエラーを返すことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestDeleteExpiredArticlesHonorsCancellation(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("SQL モックを作成できません: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deleted, err := New(db).DeleteExpiredArticles(ctx)
	if deleted != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("削除結果 = (%d, %v), 削除なしとキャンセルエラーを期待", deleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL 期待値が満たされません: %v", err)
	}
}
