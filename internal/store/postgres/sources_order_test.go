package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

/*
TestListSourcesUsesStoredDisplayOrder は、管理画面向け取得対象一覧の表示順序を検証します。

機能:
  - 未削除の取得対象を display_order、ID の順で問い合わせることを確認する
  - Repository が DB から返された表示順序を維持することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestListSourcesUsesStoredDisplayOrder(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(
		`(?s)SELECT.*FROM news_sources.*WHERE deleted_at IS NULL.*` +
			`ORDER BY display_order, id`,
	).
		WillReturnRows(
			sqlmock.NewRows([]string{
				"id",
				"name",
				"kind",
				"endpoint_url",
				"enabled",
				"fetch_interval_minutes",
				"max_items",
				"settings",
				"last_fetched_at",
				"next_fetch_at",
				"last_error",
				"created_at",
				"updated_at",
			}).
				AddRow(
					int64(9),
					"先頭の取得対象",
					"rss",
					"https://example.com/first.xml",
					false,
					15,
					20,
					[]byte(`{"fetch_full_article":false}`),
					nil,
					nil,
					"",
					now,
					now,
				).
				AddRow(
					int64(2),
					"後続の取得対象",
					"reuters",
					"https://jp.reuters.com",
					true,
					30,
					10,
					[]byte(`{"section_id":"/economy/"}`),
					nil,
					nil,
					"",
					now,
					now,
				),
		).
		RowsWillBeClosed()

	sources, err := store.ListSources(context.Background())
	if err != nil {
		t.Fatalf("ListSources() error = %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("ListSources() length = %d, 期待値は 2", len(sources))
	}
	if sources[0].ID != 9 || sources[1].ID != 2 {
		t.Errorf("ListSources() IDs = [%d, %d], DB の表示順序維持を期待", sources[0].ID, sources[1].ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderCommitsCompleteOrder は、取得対象全件の表示順序更新を検証します。

機能:
  - 未削除取得対象をロックして現在の ID 一覧を確認することを検証する
  - 指定配列の順番を 1 から始まる display_order として同一トランザクションで確定することを検証する
  - 表示順序と同時に設定更新日時を記録することを検証する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestUpdateSourceOrderCommitsCompleteOrder(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectBegin()
	expectCurrentSourceIDs(mock, 1, 2, 3)
	expectSourceOrderUpdate(mock, 3, 1)
	expectSourceOrderUpdate(mock, 1, 2)
	expectSourceOrderUpdate(mock, 2, 3)
	mock.ExpectCommit()

	if err := store.UpdateSourceOrder(context.Background(), []int64{3, 1, 2}); err != nil {
		t.Fatalf("UpdateSourceOrder() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderRejectsInvalidInput は、DB 操作前の表示順序入力検証を確認します。

機能:
  - 0 以下の ID と重複 ID を不正な表示順序として拒否する
  - 不正入力ではトランザクションを開始しないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestUpdateSourceOrderRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		sourceIDs []int64
	}{
		{name: "ゼロ", sourceIDs: []int64{0, 1}},
		{name: "負数", sourceIDs: []int64{-1, 2}},
		{name: "重複", sourceIDs: []int64{1, 1}},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New() error = %v", err)
			}
			defer db.Close()

			err = New(db).UpdateSourceOrder(context.Background(), testCase.sourceIDs)
			if !errors.Is(err, ErrInvalidSourceOrder) {
				t.Fatalf("UpdateSourceOrder() error = %v, ErrInvalidSourceOrder を期待", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("不正入力で DB へアクセスしました: %v", err)
			}
		})
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderRejectsStaleCompleteOrder は、現在の未削除一覧と一致しない順序を検証します。

機能:
  - 欠落または未知 ID を含む順序を ErrInvalidSourceOrder として拒否する
  - 一覧不一致時は表示順序を更新せずトランザクションをロールバックする

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestUpdateSourceOrderRejectsStaleCompleteOrder(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		currentIDs []int64
		requested  []int64
	}{
		{name: "欠落", currentIDs: []int64{1, 2}, requested: []int64{1}},
		{name: "未知ID", currentIDs: []int64{1, 2}, requested: []int64{1, 3}},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New() error = %v", err)
			}
			defer db.Close()
			store := New(db)

			mock.ExpectBegin()
			expectCurrentSourceIDs(mock, testCase.currentIDs...)
			mock.ExpectRollback()

			err = store.UpdateSourceOrder(context.Background(), testCase.requested)
			if !errors.Is(err, ErrInvalidSourceOrder) {
				t.Fatalf("UpdateSourceOrder() error = %v, ErrInvalidSourceOrder を期待", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("SQL期待値が満たされません: %v", err)
			}
		})
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderRollsBackUpdateFailure は、途中の表示順序更新失敗を検証します。

機能:
  - 一部の UPDATE が失敗した場合にトランザクション全体をロールバックする
  - DB エラーを呼び出し元へ返すことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestUpdateSourceOrderRollsBackUpdateFailure(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectBegin()
	expectCurrentSourceIDs(mock, 1, 2)
	expectSourceOrderUpdate(mock, 2, 1)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE news_sources")).
		WithArgs(int64(1), int64(2)).
		WillReturnError(errors.New("更新失敗"))
	mock.ExpectRollback()

	err = store.UpdateSourceOrder(context.Background(), []int64{2, 1})
	if err == nil || errors.Is(err, ErrInvalidSourceOrder) {
		t.Fatalf("UpdateSourceOrder() error = %v, DB 更新エラーを期待", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
expectCurrentSourceIDs は、表示順序更新前の未削除取得対象ロックを sqlmock へ登録します。

機能:
  - 未削除取得対象を ID 順に FOR UPDATE で読み込む SQL を期待する
  - 指定 ID を現在の取得対象一覧として返す

引数:
  - mock sqlmock.Sqlmock: SQL 期待値を登録するモック
  - sourceIDs ...int64: 現在存在する未削除取得対象 ID

返り値:
  - なし
*/
func expectCurrentSourceIDs(mock sqlmock.Sqlmock, sourceIDs ...int64) {
	rows := sqlmock.NewRows([]string{"id"})
	for _, sourceID := range sourceIDs {
		rows.AddRow(sourceID)
	}
	mock.ExpectQuery(
		`(?s)SELECT id.*FROM news_sources.*WHERE deleted_at IS NULL.*` +
			`ORDER BY id.*FOR UPDATE`,
	).
		WillReturnRows(rows).
		RowsWillBeClosed()
}

// ----------------------------------------

/*
expectSourceOrderUpdate は、単一取得対象の表示順序更新を sqlmock へ登録します。

機能:
  - 未削除の指定 ID へ 1 始まりの display_order を保存する SQL を期待する

引数:
  - mock sqlmock.Sqlmock: SQL 期待値を登録するモック
  - sourceID int64: 更新する取得対象 ID
  - displayOrder int64: 保存する 1 始まりの表示順序

返り値:
  - なし
*/
func expectSourceOrderUpdate(mock sqlmock.Sqlmock, sourceID, displayOrder int64) {
	mock.ExpectExec(
		`(?s)UPDATE news_sources.*SET display_order = \$2,.*`+
			`updated_at = CURRENT_TIMESTAMP.*WHERE id = \$1 AND deleted_at IS NULL`,
	).
		WithArgs(sourceID, displayOrder).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
