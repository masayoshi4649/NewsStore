package web

import (
	"bytes"
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

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

/*
TestSourceOrderTemplateProvidesAccessibleControls は、取得対象一覧の並べ替え操作を検証します。

機能:
  - 一覧の最左列へドラッグハンドルを配置することを確認する
  - 各行へ取得対象 ID を設定し、支援技術向けの説明と状態通知を提供することを確認する
  - 取得対象一覧専用 JavaScript を遅延読込することを確認する

引数:
  - t *testing.T: テンプレート出力を検証するテスト状態

返り値:
  - なし
*/
func TestSourceOrderTemplateProvidesAccessibleControls(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page := sourceListPage{
		Base: basePage{
			Title:     "取得対象",
			CSRFToken: "csrf-test-token",
			Active:    "sources",
		},
		Sources: []sourceListItem{
			{
				Source: domain.Source{
					ID:                   17,
					Name:                 "先頭の取得対象",
					Kind:                 domain.SourceKindRSS,
					Enabled:              true,
					FetchIntervalMinutes: 15,
					MaxItems:             20,
				},
				Description: "https://example.com/first.xml",
			},
			{
				Source: domain.Source{
					ID:                   29,
					Name:                 "末尾の取得対象",
					Kind:                 domain.SourceKindRSS,
					Enabled:              false,
					FetchIntervalMinutes: 30,
					MaxItems:             10,
				},
				Description: "https://example.com/last.xml",
			},
		},
	}

	var output bytes.Buffer
	if err := server.template.ExecuteTemplate(&output, "sources.html", page); err != nil {
		t.Fatalf("sources.html の実行に失敗しました: %v", err)
	}
	html := output.String()
	wantValues := []string{
		`id="source-order-description"`,
		`id="source-order-status"`,
		`role="status"`,
		`aria-live="polite"`,
		`id="source-order-csrf"`,
		`name="_csrf"`,
		`value="csrf-test-token"`,
		`id="source-order-list"`,
		`class="source-order-row" data-source-id="17"`,
		`class="source-order-row" data-source-id="29"`,
		`class="source-drag-handle"`,
		`aria-label="先頭の取得対象の表示順を変更"`,
		`aria-describedby="source-order-description"`,
		`aria-hidden="true"`,
		`src="/static/source-list.js" defer`,
	}
	for _, want := range wantValues {
		if !strings.Contains(html, want) {
			t.Errorf("取得対象一覧 HTML に %q がありません", want)
		}
	}

	handleColumnIndex := strings.Index(html, `class="source-order-column"`)
	nameColumnIndex := strings.Index(html, "<th>名前</th>")
	if handleColumnIndex < 0 || nameColumnIndex < 0 || handleColumnIndex > nameColumnIndex {
		t.Error("ドラッグハンドル列が名前列より左にありません")
	}
}

// ----------------------------------------

/*
TestSourceListJavaScriptAssetProvidesDragAndKeyboardOrdering は、取得対象並べ替え JavaScript を検証します。

機能:
  - Pointer Events によるドラッグ操作を提供することを確認する
  - 画面端で requestAnimationFrame による継続スクロールを提供することを確認する
  - キーボードの上下移動と先頭・末尾移動を提供することを確認する
  - CSRF と反復 source_ids を URL エンコードして並べ替え API へ送ることを確認する
  - 保存失敗時にサーバーの案内を読み取ることを確認する
  - 更新中の資産を長期キャッシュしないことを確認する

引数:
  - t *testing.T: 静的 JavaScript 配信を検証するテスト状態

返り値:
  - なし
*/
func TestSourceListJavaScriptAssetProvidesDragAndKeyboardOrdering(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/static/source-list.js", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(
		contentType,
		"javascript",
	) {
		t.Errorf("Content-Type = %q, JavaScript を期待", contentType)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Errorf("Cache-Control = %q, 期待値は no-cache", cacheControl)
	}
	wantValues := []string{
		"initializeSourceOrder",
		"startSourceOrderDrag",
		"moveSourceOrderDrag",
		"finishSourceOrderDrag",
		"moveSourceOrderByKeyboard",
		"saveSourceOrder",
		"restoreSourceOrder",
		"setSourceOrderSaving",
		"announceSourceOrder",
		"getSourceOrderIDs",
		"pointerdown",
		"pointermove",
		"pointerup",
		"pointercancel",
		"requestAnimationFrame",
		"cancelAnimationFrame",
		"ArrowUp",
		"ArrowDown",
		`"Home"`,
		`"End"`,
		"URLSearchParams",
		"response.json()",
		"source_ids",
		"_csrf",
		"/admin/sources/order",
	}
	for _, want := range wantValues {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("/static/source-list.js に %q がありません", want)
		}
	}
	if !strings.Contains(
		recorder.Header().Get("Content-Security-Policy"),
		"script-src 'self'",
	) {
		t.Error("CSP が同一オリジン JavaScript に制限されていません")
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderRequiresCSRF は、表示順保存 API の CSRF 保護を検証します。

機能:
  - CSRF Cookie とフォームトークンがない POST を Repository 呼出し前に拒否することを確認する

引数:
  - t *testing.T: HTTP 応答と DB 未呼出しを検証するテスト状態

返り値:
  - なし
*/
func TestUpdateSourceOrderRequiresCSRF(t *testing.T) {
	handler, _ := newSourceOrderHTTPTestServer(t)
	values := url.Values{}
	values.Add("source_ids", "1")
	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/sources/order",
		strings.NewReader(values.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusForbidden)
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderRejectsInvalidIDs は、不正な表示順入力を検証します。

機能:
  - 未指定、整数以外、0、負数、64 ビット超過、重複 ID を 400 の JSON で拒否する
  - 画面 GET で取得した CSRF Cookie と同じトークンをフォームへ設定して POST する
  - 入力検証失敗時に Repository を呼び出さないことを確認する

引数:
  - t *testing.T: 入力別の HTTP 応答を検証するテスト状態

返り値:
  - なし
*/
func TestUpdateSourceOrderRejectsInvalidIDs(t *testing.T) {
	testCases := []struct {
		name      string
		sourceIDs []string
	}{
		{name: "未指定", sourceIDs: nil},
		{name: "空文字", sourceIDs: []string{""}},
		{name: "整数以外", sourceIDs: []string{"abc"}},
		{name: "ゼロ", sourceIDs: []string{"0"}},
		{name: "負数", sourceIDs: []string{"-1"}},
		{name: "64ビット超過", sourceIDs: []string{"9223372036854775808"}},
		{name: "重複", sourceIDs: []string{"7", "7"}},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			handler, _ := newSourceOrderHTTPTestServer(t)
			csrfCookie := acquireSourceOrderCSRFCookie(t, handler)

			recorder := postSourceOrder(t, handler, csrfCookie, testCase.sourceIDs)

			assertSourceOrderJSONError(t, recorder, http.StatusBadRequest)
		})
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderSavesCompleteOrder は、取得対象の表示順保存成功を検証します。

機能:
  - CSRF 取得後の反復 source_ids を入力順のまま Repository へ渡す
  - 未削除の全取得対象をロックし、1 始まりの表示順で更新してコミットする
  - 成功時にキャッシュ禁止の JSON を返すことを確認する

引数:
  - t *testing.T: SQL と HTTP 応答を検証するテスト状態

返り値:
  - なし
*/
func TestUpdateSourceOrderSavesCompleteOrder(t *testing.T) {
	handler, mock := newSourceOrderHTTPTestServer(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT id FROM news_sources WHERE deleted_at IS NULL ORDER BY id FOR UPDATE",
	)).WillReturnRows(
		sqlmock.NewRows([]string{"id"}).
			AddRow(int64(1)).
			AddRow(int64(2)).
			AddRow(int64(3)),
	)
	expectSourceOrderUpdate(mock, 3, 1)
	expectSourceOrderUpdate(mock, 1, 2)
	expectSourceOrderUpdate(mock, 2, 3)
	mock.ExpectCommit()
	csrfCookie := acquireSourceOrderCSRFCookie(t, handler)

	recorder := postSourceOrder(t, handler, csrfCookie, []string{"3", "1", "2"})

	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"状態コード = %d, 期待値は %d: %s",
			recorder.Code,
			http.StatusOK,
			recorder.Body.String(),
		)
	}
	assertSourceOrderJSONContentType(t, recorder)
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, 期待値は no-store", cacheControl)
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("成功 JSON を解析できません: %v", err)
	}
	if response.Message == "" {
		t.Error("成功 JSON の message が空です")
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderReturnsConflictForStaleList は、画面表示後に一覧が変化した場合を検証します。

機能:
  - POST された ID 集合が未削除の全取得対象と一致しない場合に更新せずロールバックする
  - 再読込可能な 409 の JSON エラーを返すことを確認する

引数:
  - t *testing.T: 競合時の SQL と HTTP 応答を検証するテスト状態

返り値:
  - なし
*/
func TestUpdateSourceOrderReturnsConflictForStaleList(t *testing.T) {
	handler, mock := newSourceOrderHTTPTestServer(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT id FROM news_sources WHERE deleted_at IS NULL ORDER BY id FOR UPDATE",
	)).WillReturnRows(
		sqlmock.NewRows([]string{"id"}).
			AddRow(int64(1)).
			AddRow(int64(2)).
			AddRow(int64(3)),
	)
	mock.ExpectRollback()
	csrfCookie := acquireSourceOrderCSRFCookie(t, handler)

	recorder := postSourceOrder(t, handler, csrfCookie, []string{"2", "1"})

	assertSourceOrderJSONError(t, recorder, http.StatusConflict)
	if !strings.Contains(recorder.Body.String(), "再読み込み") {
		t.Error("競合応答に画面再読み込みの案内がありません")
	}
}

// ----------------------------------------

/*
TestUpdateSourceOrderMasksRepositoryFailure は、表示順保存時の DB 異常を検証します。

機能:
  - 未削除取得対象のロック取得に失敗した場合にトランザクションをロールバックする
  - 内部 DB エラーを応答へ漏らさず 500 の JSON エラーを返すことを確認する

引数:
  - t *testing.T: DB 異常時の SQL と HTTP 応答を検証するテスト状態

返り値:
  - なし
*/
func TestUpdateSourceOrderMasksRepositoryFailure(t *testing.T) {
	handler, mock := newSourceOrderHTTPTestServer(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT id FROM news_sources WHERE deleted_at IS NULL ORDER BY id FOR UPDATE",
	)).WillReturnError(errors.New("テスト用の内部 DB エラー"))
	mock.ExpectRollback()
	csrfCookie := acquireSourceOrderCSRFCookie(t, handler)

	recorder := postSourceOrder(t, handler, csrfCookie, []string{"2", "1"})

	assertSourceOrderJSONError(t, recorder, http.StatusInternalServerError)
	if strings.Contains(recorder.Body.String(), "テスト用の内部 DB エラー") {
		t.Error("内部 DB エラーが HTTP 応答へ含まれています")
	}
}

// ----------------------------------------

/*
newSourceOrderHTTPTestServer は、表示順 API 用の Web サーバーと SQL モックを生成します。

機能:
  - PostgreSQL Store を sqlmock へ接続する
  - ログを破棄するテスト用 Web サーバーを構築する
  - テスト終了時に SQL 期待値と DB クローズを確認する

引数:
  - t *testing.T: 後処理とエラー報告を登録するテスト状態

返り値:
  - http.Handler: 表示順 API を含む HTTP ハンドラー
  - sqlmock.Sqlmock: SQL 期待値を登録するモック
*/
func newSourceOrderHTTPTestServer(t *testing.T) (http.Handler, sqlmock.Sqlmock) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := db.Close(); err != nil {
			t.Errorf("テスト DB を閉じられません: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("DB 期待値を満たしていません: %v", err)
		}
	})

	server, err := New(
		postgres.New(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server.Handler(), mock
}

// ----------------------------------------

/*
acquireSourceOrderCSRFCookie は、取得対象入力画面から CSRF Cookie を取得します。

機能:
  - GET /admin/sources/new を実行して既存 CSRF ミドルウェアを通す
  - Cookie と HTML の hidden 項目へ同じトークンが設定されることを確認する

引数:
  - t *testing.T: HTTP 応答を検証するテスト状態
  - handler http.Handler: 管理画面の HTTP ハンドラー

返り値:
  - *http.Cookie: POST へ引き継ぐ CSRF Cookie
*/
func acquireSourceOrderCSRFCookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/admin/sources/new", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"CSRF 取得画面の状態コード = %d, 期待値は %d",
			recorder.Code,
			http.StatusOK,
		)
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name != csrfCookieName {
			continue
		}
		if cookie.Value == "" {
			t.Fatal("CSRF Cookie が空です")
		}
		wantHidden := `name="_csrf" value="` + cookie.Value + `"`
		if !strings.Contains(recorder.Body.String(), wantHidden) {
			t.Errorf("画面に CSRF Cookie と同じ hidden 値がありません")
		}
		return cookie
	}
	t.Fatal("CSRF Cookie が発行されませんでした")
	return nil
}

// ----------------------------------------

/*
postSourceOrder は、CSRF 付きの表示順フォームを POST します。

機能:
  - source_ids を入力順の反復フォーム項目として URL エンコードする
  - 画面 GET で取得した CSRF Cookie とフォームトークンを送信する

引数:
  - t *testing.T: リクエスト生成失敗を報告するテスト状態
  - handler http.Handler: 表示順 API を含む HTTP ハンドラー
  - csrfCookie *http.Cookie: 画面 GET で取得した CSRF Cookie
  - sourceIDs []string: 送信順の取得対象 ID

返り値:
  - *httptest.ResponseRecorder: API の HTTP 応答
*/
func postSourceOrder(
	t *testing.T,
	handler http.Handler,
	csrfCookie *http.Cookie,
	sourceIDs []string,
) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{}
	values.Set(csrfFormName, csrfCookie.Value)
	for _, sourceID := range sourceIDs {
		values.Add("source_ids", sourceID)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/sources/order",
		strings.NewReader(values.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.AddCookie(csrfCookie)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)
	return recorder
}

// ----------------------------------------

/*
expectSourceOrderUpdate は、1 件分の表示順 UPDATE を SQL モックへ登録します。

機能:
  - 取得対象 ID と 1 始まりの表示順を引数に持つ更新を期待する
  - 1 行が正常に更新された結果を返す

引数:
  - mock sqlmock.Sqlmock: SQL 期待値を登録するモック
  - sourceID int64: 更新する取得対象 ID
  - displayOrder int64: 保存する 1 始まりの表示順

返り値:
  - なし
*/
func expectSourceOrderUpdate(mock sqlmock.Sqlmock, sourceID, displayOrder int64) {
	mock.ExpectExec(regexp.QuoteMeta(
		"UPDATE news_sources SET display_order = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND deleted_at IS NULL",
	)).WithArgs(sourceID, displayOrder).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// ----------------------------------------

/*
assertSourceOrderJSONError は、表示順 API の JSON エラー応答を検証します。

機能:
  - 状態コード、Content-Type、no-store を確認する
  - 利用者向け error メッセージが空でないことを確認する

引数:
  - t *testing.T: HTTP 応答の不一致を報告するテスト状態
  - recorder *httptest.ResponseRecorder: 検証する API 応答
  - wantStatus int: 期待する HTTP 状態コード

返り値:
  - なし
*/
func assertSourceOrderJSONError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf(
			"状態コード = %d, 期待値は %d: %s",
			recorder.Code,
			wantStatus,
			recorder.Body.String(),
		)
	}
	assertSourceOrderJSONContentType(t, recorder)
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, 期待値は no-store", cacheControl)
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("エラー JSON を解析できません: %v", err)
	}
	if response.Error == "" {
		t.Error("エラー JSON の error が空です")
	}
}

// ----------------------------------------

/*
assertSourceOrderJSONContentType は、表示順 API の JSON Content-Type を検証します。

機能:
  - HTTP 応答が application/json として返されることを確認する

引数:
  - t *testing.T: Content-Type の不一致を報告するテスト状態
  - recorder *httptest.ResponseRecorder: 検証する API 応答

返り値:
  - なし
*/
func assertSourceOrderJSONContentType(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(
		contentType,
		"application/json",
	) {
		t.Errorf("Content-Type = %q, application/json を期待", contentType)
	}
}
