package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/masayoshi4649/NewsStore/internal/domain"
)

/*
TestArticleRevisionExists は、複合キーによる保存済みリビジョン確認を検証します。

機能:
  - 取得対象 ID、外部記事 ID、リビジョン ID による存在確認結果を検証します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestArticleRevisionExists(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS")).
		WithArgs(int64(1), "external-1", "revision-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	exists, err := store.ArticleRevisionExists(
		context.Background(),
		1,
		"external-1",
		"revision-1",
	)
	if err != nil {
		t.Fatalf("ArticleRevisionExists() error = %v", err)
	}
	if !exists {
		t.Error("ArticleRevisionExists() = false, 期待値は true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestInsertArticleReturnsInserted は、INSERT成功時に新規保存を返すことを検証します。

機能:
  - 記事の挿入成功時に新規保存済みとして判定されることを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestInsertArticleReturnsInserted(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)
	article := testArticle()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO news_articles")).
		WithArgs(
			article.SourceID,
			article.Provider,
			article.ExternalID,
			article.RevisionID,
			article.CanonicalURL,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			article.Headline,
			article.BodyText,
			string(article.RawPayload),
			false,
		).
		WillReturnRows(sqlmock.NewRows([]string{"inserted"}).AddRow(true))

	inserted, err := store.InsertArticle(context.Background(), article)
	if err != nil {
		t.Fatalf("InsertArticle() error = %v", err)
	}
	if !inserted {
		t.Error("InsertArticle() inserted = false, 期待値は true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestInsertArticleReturnsSkippedOnConflict は、競合時の空RETURNINGを重複として扱うことを検証します。

機能:
  - 一意制約競合で行が返らない場合に既存記事として扱われることを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestInsertArticleReturnsSkippedOnConflict(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)
	article := testArticle()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO news_articles")).
		WithArgs(
			article.SourceID,
			article.Provider,
			article.ExternalID,
			article.RevisionID,
			article.CanonicalURL,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			article.Headline,
			article.BodyText,
			string(article.RawPayload),
			false,
		).
		WillReturnRows(sqlmock.NewRows([]string{"inserted"}))

	inserted, err := store.InsertArticle(context.Background(), article)
	if err != nil {
		t.Fatalf("InsertArticle() error = %v", err)
	}
	if inserted {
		t.Error("InsertArticle() inserted = true, 期待値は false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestInsertArticleUsesEmptyJSONObject は、原データ未設定時に有効な空JSONを保存することを検証します。

機能:
  - 原データが未設定の記事を空の JSON オブジェクトへ補正して保存することを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestInsertArticleUsesEmptyJSONObject(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	store := New(db)
	article := testArticle()
	article.RawPayload = nil

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO news_articles")).
		WithArgs(
			article.SourceID,
			article.Provider,
			article.ExternalID,
			article.RevisionID,
			article.CanonicalURL,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			article.Headline,
			article.BodyText,
			"{}",
			false,
		).
		WillReturnError(sql.ErrNoRows)

	inserted, err := store.InsertArticle(context.Background(), article)
	if err != nil {
		t.Fatalf("InsertArticle() error = %v", err)
	}
	if inserted {
		t.Error("InsertArticle() inserted = true, 期待値は false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
TestInsertArticleReturnsFalseOnOverwrite は、上書きを新規保存として数えないことを検証します。

機能:
  - DB が上書き済みの行を返した場合の返り値を確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestInsertArticleReturnsFalseOnOverwrite(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("SQL モックを作成できません: %v", err)
	}
	defer db.Close()
	article := testArticle()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO news_articles")).
		WithArgs(article.SourceID, article.Provider, article.ExternalID, article.RevisionID,
			article.CanonicalURL, sqlmock.AnyArg(), sqlmock.AnyArg(), article.Headline,
			article.BodyText, string(article.RawPayload), false).
		WillReturnRows(sqlmock.NewRows([]string{"inserted"}).AddRow(false))

	inserted, err := New(db).InsertArticle(context.Background(), article)
	if err != nil {
		t.Fatalf("記事を上書きできません: %v", err)
	}
	if inserted {
		t.Error("上書きの返り値が新規保存になっています")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL 期待値が満たされません: %v", err)
	}
}

// ----------------------------------------

/*
testArticle は、Repositoryテスト用の有効な記事を生成します。

機能:
  - 記事 Repository のテストで共通利用する全項目設定済みの記事を生成します。

引数:
  - なし

返り値:
  - domain.Article: 全保存項目を持つテスト記事
*/
func testArticle() domain.Article {
	publishedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Hour)
	updatedAt := publishedAt.Add(time.Hour)
	return domain.Article{
		SourceID:     1,
		Provider:     "rss",
		ExternalID:   "external-1",
		RevisionID:   "revision-1",
		CanonicalURL: "https://example.com/articles/1",
		PublishedAt:  &publishedAt,
		UpdatedAt:    &updatedAt,
		Headline:     "記事見出し",
		BodyText:     "記事本文",
		RawPayload:   json.RawMessage(`{"id":"external-1"}`),
	}
}
