package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/articlesearch"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

/*
TestListArticlesAPIAppliesAllConditions は、記事一覧APIの複合AND検索を検証します。

機能:
  - 取得対象、逆順の日時両端、複数検索語、件数、カーソルを共通Repositoryへ渡すことを確認する
  - 逆順の日時両端が時系列順へ正規化されることを確認する
  - 実効日時降順の概要と次ページカーソルをJSONで返すことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestListArticlesAPIAppliesAllConditions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	dateFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	dateTo := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	beforeTime := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	effectiveAt := beforeTime.Add(-time.Hour)
	rows := sqlmock.NewRows([]string{
		"id",
		"name",
		"source_id",
		"provider",
		"external_id",
		"revision_id",
		"canonical_url",
		"published_at",
		"updated_at",
		"headline",
		"body_text",
		"fetched_at",
		"effective_at",
	}).
		AddRow(
			int64(87),
			"Reuters経済",
			int64(7),
			"reuters",
			"external-87",
			"revision-87",
			"https://example.com/87",
			effectiveAt,
			effectiveAt,
			"経済記事",
			"企業の本文",
			effectiveAt,
			effectiveAt,
		).
		AddRow(
			int64(86),
			"Reuters経済",
			int64(7),
			"reuters",
			"external-86",
			"revision-86",
			"https://example.com/86",
			effectiveAt.Add(-time.Hour),
			effectiveAt,
			"経済記事2",
			"企業の本文2",
			effectiveAt,
			effectiveAt.Add(-time.Hour),
		)

	mock.ExpectQuery(
		`(?s)SELECT.*FROM news_articles a.*`+
			`AND a\.source_id = \$1.*`+
			`COALESCE\(a\.published_at, a\.fetched_at\) >= \$2.*`+
			`COALESCE\(a\.published_at, a\.fetched_at\) <= \$3.*`+
			`a\.headline ILIKE.*\$4.*`+
			`a\.headline ILIKE.*\$5.*`+
			`\(COALESCE\(a\.published_at, a\.fetched_at\), a\.id\) < \(\$6, \$7\).*`+
			`ORDER BY COALESCE\(a\.published_at, a\.fetched_at\) DESC, a\.id DESC.*`+
			`LIMIT \$8`,
	).
		WithArgs(
			int64(7),
			dateFrom,
			dateTo,
			"経済",
			"企業",
			beforeTime,
			int64(88),
			2,
		).
		WillReturnRows(rows)

	server, err := New(
		postgres.New(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		20,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	query := url.Values{}
	query.Set("source_id", "7")
	query.Set("date_from", dateTo.Format(time.RFC3339))
	query.Set("date_to", dateFrom.Format(time.RFC3339))
	query.Set("query", "経済")
	query.Add("keywords", "企業")
	query.Set("limit", "1")
	query.Set("cursor", articlesearch.EncodeCursor(beforeTime, 88))
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/articles?"+query.Encode(),
		nil,
	)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 本文 = %s", recorder.Code, recorder.Body.String())
	}
	var response articleAPIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("JSONを解析できません: %v", err)
	}
	if len(response.Articles) != 1 || response.Articles[0].ID != 87 {
		t.Fatalf("記事一覧 = %#v, ID=87の1件を期待", response.Articles)
	}
	if response.Articles[0].BodyExcerpt != "企業の本文" {
		t.Errorf("body_excerpt = %q", response.Articles[0].BodyExcerpt)
	}
	if !response.HasMore || response.NextCursor == "" {
		t.Errorf("ページ情報 = has_more:%v next_cursor:%q", response.HasMore, response.NextCursor)
	}
	cursorTime, cursorID, err := articlesearch.DecodeCursor(response.NextCursor)
	if err != nil {
		t.Fatalf("next_cursorを解析できません: %v", err)
	}
	if cursorID != 87 || cursorTime == nil || !cursorTime.Equal(effectiveAt) {
		t.Errorf("next_cursor = time:%v id:%d", cursorTime, cursorID)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, 期待値は no-store", cacheControl)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB期待値を満たしていません: %v", err)
	}
}

// ----------------------------------------

/*
TestListArticlesAPIUsesConfiguredDefaultWithoutConditions は、無条件検索の設定件数を検証します。

機能:
  - 検索条件を一切指定しない場合に設定由来の件数で最新記事を取得することを確認する
  - Repositoryが判定用の1件を加えたLIMITだけを受け取ることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestListArticlesAPIUsesConfiguredDefaultWithoutConditions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(
		`(?s)SELECT.*FROM news_articles a.*` +
			`ORDER BY COALESCE\(a\.published_at, a\.fetched_at\) DESC, a\.id DESC.*` +
			`LIMIT \$1`,
	).
		WithArgs(8).
		WillReturnRows(sqlmock.NewRows([]string{
			"id",
			"name",
			"source_id",
			"provider",
			"external_id",
			"revision_id",
			"canonical_url",
			"published_at",
			"updated_at",
			"headline",
			"body_text",
			"fetched_at",
			"effective_at",
		}))

	server, err := New(
		postgres.New(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		7,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/articles", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 本文 = %s", recorder.Code, recorder.Body.String())
	}
	var response articleAPIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("JSONを解析できません: %v", err)
	}
	if response.Articles == nil || len(response.Articles) != 0 {
		t.Errorf("articles = %#v, 空配列を期待", response.Articles)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB期待値を満たしていません: %v", err)
	}
}

// ----------------------------------------

/*
TestListArticlesAPIRejectsInvalidConditions は、記事一覧APIの入力検証を確認します。

機能:
  - 過大件数、6語の検索、不正なカーソルと本文範囲をDBアクセス前に拒否する
  - 利用者へ内部情報を含まないJSONの400エラーを返す

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestListArticlesAPIRejectsInvalidConditions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()
	server, err := New(postgres.New(db), nil, nil, 20)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	testCases := []string{
		"/api/articles?limit=0",
		"/api/articles?limit=101",
		"/api/articles?keywords=1&keywords=2&keywords=3&keywords=4&keywords=5&keywords=6",
		"/api/articles?cursor=invalid",
		"/api/articles/1?body_offset=10000001",
		"/api/articles/1?body_limit=16001",
	}
	for _, requestURL := range testCases {
		request := httptest.NewRequest(http.MethodGet, requestURL, nil)
		recorder := httptest.NewRecorder()

		server.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf(
				"URL=%s 状態コード = %d, 期待値は400",
				requestURL,
				recorder.Code,
			)
		}
		var response map[string]string
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Errorf("URL=%s JSONを解析できません: %v", requestURL, err)
		}
		if response["error"] == "" {
			t.Errorf("URL=%s errorが空です", requestURL)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("入力不正時にDBへアクセスしました: %v", err)
	}
}

// ----------------------------------------

/*
TestShowArticleAPIReturnsBodyChunk は、記事詳細APIが本文断片を返すことを検証します。

機能:
  - MCPと同じRepositoryから指定範囲の本文だけを読み込む
  - 続きの本文がある場合に次の本文位置を返す
  - 公開日時を実効日時としてJSONへ設定する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestShowArticleAPIReturnsBodyChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	publishedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	fetchedAt := publishedAt.Add(time.Minute)
	mock.ExpectQuery(
		`(?s)`+regexp.QuoteMeta(`SUBSTRING(a.body_text FROM $2 + 1 FOR $3 + 1)`)+
			`.*FROM news_articles a`,
	).
		WithArgs(int64(42), 3, 5).
		WillReturnRows(sqlmock.NewRows([]string{
			"id",
			"name",
			"source_id",
			"provider",
			"external_id",
			"revision_id",
			"canonical_url",
			"published_at",
			"updated_at",
			"headline",
			"body_text",
			"fetched_at",
		}).AddRow(
			int64(42),
			"Reuters",
			int64(3),
			"reuters",
			"external-42",
			"revision-42",
			"https://example.com/42",
			publishedAt,
			publishedAt,
			"記事見出し",
			"123456",
			fetchedAt,
		))

	server, err := New(postgres.New(db), nil, nil, 20)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/articles/42?body_offset=3&body_limit=5",
		nil,
	)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 本文 = %s", recorder.Code, recorder.Body.String())
	}
	var response articleAPIDetailResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("JSONを解析できません: %v", err)
	}
	if response.Article.ID != 42 || response.Article.BodyText != "12345" {
		t.Errorf("article = %#v", response.Article)
	}
	if response.Article.BodyOffset != 3 ||
		!response.Article.HasMore ||
		response.Article.NextBodyOffset != 8 {
		t.Errorf(
			"本文ページ情報 = offset:%d has_more:%v next:%d",
			response.Article.BodyOffset,
			response.Article.HasMore,
			response.Article.NextBodyOffset,
		)
	}
	if response.Article.EffectiveAt != publishedAt.Format(time.RFC3339Nano) {
		t.Errorf("effective_at = %q", response.Article.EffectiveAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB期待値を満たしていません: %v", err)
	}
}

// ----------------------------------------

/*
TestListSourcesAPIReturnsSettingNamesAndIDs は、取得対象一覧APIを検証します。

機能:
  - 設定名とnews_sourcesのIDを含む軽量な取得対象一覧をJSONで返す
  - 無効な取得対象も有効状態付きで返す

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestListSourcesAPIReturnsSettingNamesAndIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(
		`(?s)SELECT id, name, kind, enabled.*FROM news_sources.*deleted_at IS NULL`,
	).
		WillReturnRows(sqlmock.NewRows([]string{
			"id",
			"name",
			"kind",
			"enabled",
		}).AddRow(int64(5), "Reuters経済", "reuters", false))

	server, err := New(postgres.New(db), nil, nil, 20)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/sources", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 本文 = %s", recorder.Code, recorder.Body.String())
	}
	var response sourceAPIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("JSONを解析できません: %v", err)
	}
	if len(response.Sources) != 1 ||
		response.Sources[0].ID != 5 ||
		response.Sources[0].Name != "Reuters経済" {
		t.Errorf("sources = %#v", response.Sources)
	}
	if response.Sources[0].Enabled {
		t.Error("無効な取得対象が有効として返されました")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB期待値を満たしていません: %v", err)
	}
}

// ----------------------------------------

/*
TestArticlePagesRenderWithoutDatabaseAccess は、記事画面がAPI駆動のシェルであることを検証します。

機能:
  - DBを持たないWebサーバーでも記事一覧と正しいIDの記事詳細画面を描画できることを確認する
  - 不正な記事IDの画面だけは404として扱う

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestArticlePagesRenderWithoutDatabaseAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil, 20)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	testCases := []struct {
		url             string
		statusCode      int
		wantValues      []string
		forbiddenValues []string
	}{
		{
			url:        "/admin/articles",
			statusCode: http.StatusOK,
			wantValues: []string{
				`<details id="article-filter-toggle"`,
				`class="article-date-separator" aria-hidden="true">～</span>`,
				`id="article-keyword-1"`,
				`id="article-keyword-2"`,
				`id="article-keyword-3"`,
				`id="article-keyword-4"`,
				`id="article-keyword-5"`,
				`maxlength="200"`,
				`id="article-grid"`,
				`href="/lib/gridjs/theme/mermaid.min.css"`,
				`src="/lib/axios/axios.min.js"`,
				`src="/lib/gridjs/gridjs.umd.js"`,
				`src="/static/articles.js?v=20260726-gridjs"`,
			},
			forbiddenValues: []string{
				`id="article-count"`,
				`id="article-loading"`,
				`id="article-result-status"`,
				`id="article-table-body"`,
				`id="article-load-more"`,
				`最新の記事を取得しています`,
				`続きがあります`,
				`下限`,
				`上限`,
				`<details id="article-filter-toggle" class="article-filter-disclosure" open`,
			},
		},
		{
			url:        "/admin/articles/42",
			statusCode: http.StatusOK,
			wantValues: []string{
				`id="article-detail-body"`,
				`id="article-body-load-more"`,
				`src="/lib/axios/axios.min.js"`,
			},
		},
		{
			url:        "/admin/articles/invalid",
			statusCode: http.StatusNotFound,
		},
	}
	for _, testCase := range testCases {
		request := httptest.NewRequest(http.MethodGet, testCase.url, nil)
		recorder := httptest.NewRecorder()

		server.Handler().ServeHTTP(recorder, request)

		if recorder.Code != testCase.statusCode {
			t.Errorf(
				"URL=%s 状態コード = %d, 期待値は%d",
				testCase.url,
				recorder.Code,
				testCase.statusCode,
			)
		}
		for _, value := range testCase.wantValues {
			if !strings.Contains(recorder.Body.String(), value) {
				t.Errorf("URL=%s HTMLに %q がありません", testCase.url, value)
			}
		}
		for _, value := range testCase.forbiddenValues {
			if strings.Contains(recorder.Body.String(), value) {
				t.Errorf("URL=%s HTMLに不要な %q があります", testCase.url, value)
			}
		}
	}
}

// ----------------------------------------

/*
TestArticleAPIMasksRepositoryErrorsAndReturnsNotFound は、記事APIの安全なエラー応答を検証します。

機能:
  - 記事一覧のRepositoryエラーから接続情報を公開しないことを確認する
  - 存在しない記事詳細を内部情報なしの404として返すことを確認する
  - エラー応答をキャッシュ禁止にすることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestArticleAPIMasksRepositoryErrorsAndReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("一覧エラー", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`(?s)SELECT.*FROM news_articles a`).
			WillReturnError(errors.New("password=secret"))

		server, err := New(postgres.New(db), nil, nil, 20)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		request := httptest.NewRequest(http.MethodGet, "/api/articles", nil)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("状態コード = %d, 期待値は500", recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "password") ||
			!strings.Contains(recorder.Body.String(), "記事を検索できません") {
			t.Errorf("エラー応答 = %q, 内部情報を含まない文言を期待", recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Error("Cache-Control = no-store が設定されていません")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("DB期待値を満たしていません: %v", err)
		}
	})

	t.Run("記事なし", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New() error = %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(
			`(?s)`+regexp.QuoteMeta(`SUBSTRING(a.body_text FROM $2 + 1 FOR $3 + 1)`),
		).
			WithArgs(int64(404), 0, 8000).
			WillReturnError(sql.ErrNoRows)

		server, err := New(postgres.New(db), nil, nil, 20)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		request := httptest.NewRequest(http.MethodGet, "/api/articles/404", nil)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("状態コード = %d, 期待値は404", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "記事が見つかりません") {
			t.Errorf("エラー応答 = %q", recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Error("Cache-Control = no-store が設定されていません")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("DB期待値を満たしていません: %v", err)
		}
	})
}

// ----------------------------------------

/*
TestArticleAPIRejectsRequestsWhenConcurrencyLimitIsFull は、記事API共通の同時実行数制限を検証します。

機能:
  - 記事検索、記事詳細、取得対象一覧が同じ8個の実行枠を共有することを確認する
  - 実行枠が満杯の場合にDBへアクセスせず、待機せず503のJSONエラーを返すことを確認する
  - 再試行時刻とキャッシュ禁止のHTTPヘッダーを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestArticleAPIRejectsRequestsWhenConcurrencyLimitIsFull(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil, 20)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if capacity := cap(server.articleAPISlots); capacity != maxConcurrentArticleAPIRequests {
		t.Fatalf(
			"記事API実行枠 = %d, 期待値は%d",
			capacity,
			maxConcurrentArticleAPIRequests,
		)
	}
	for index := 0; index < maxConcurrentArticleAPIRequests; index++ {
		server.articleAPISlots <- struct{}{}
	}
	defer func() {
		for index := 0; index < maxConcurrentArticleAPIRequests; index++ {
			<-server.articleAPISlots
		}
	}()

	requestURLs := []string{
		"/api/articles",
		"/api/articles/1",
		"/api/sources",
	}
	for _, requestURL := range requestURLs {
		request := httptest.NewRequest(http.MethodGet, requestURL, nil)
		recorder := httptest.NewRecorder()
		startedAt := time.Now()

		server.Handler().ServeHTTP(recorder, request)

		if elapsed := time.Since(startedAt); elapsed >= time.Second {
			t.Errorf("URL=%s 応答時間 = %s, 待機しない応答を期待", requestURL, elapsed)
		}
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf(
				"URL=%s 状態コード = %d, 期待値は503",
				requestURL,
				recorder.Code,
			)
		}
		var response map[string]string
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Errorf("URL=%s JSONを解析できません: %v", requestURL, err)
		}
		if response["error"] != "記事APIが混雑しています" {
			t.Errorf("URL=%s error = %q", requestURL, response["error"])
		}
		if recorder.Header().Get("Retry-After") != "1" {
			t.Errorf("URL=%s Retry-After = %q", requestURL, recorder.Header().Get("Retry-After"))
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Errorf(
				"URL=%s Cache-Control = %q",
				requestURL,
				recorder.Header().Get("Cache-Control"),
			)
		}
	}
}
