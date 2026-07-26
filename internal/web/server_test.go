package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/domain"
)

/*
TestNewBuildsEmbeddedAssets は、埋め込みテンプレートと CSS を解析できることを検証します。

機能:
  - 埋め込み資産を使用してWebサーバーを初期化し、HTTPハンドラーを生成できることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestNewBuildsEmbeddedAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if server.Handler() == nil {
		t.Fatal("Handler() = nil, HTTP ハンドラーを期待")
	}
}

// ----------------------------------------

/*
TestTemplatesExecute は、各管理画面テンプレートが代表データで実行できることを検証します。

機能:
  - 一覧・入力・詳細・実行履歴・エラーの各テンプレートを代表データで描画できることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestTemplatesExecute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	base := basePage{Title: "テスト", CSRFToken: "token"}
	testCases := []struct {
		name string
		data any
	}{
		{
			name: "sources.html",
			data: sourceListPage{
				Base: base,
				Sources: []sourceListItem{{
					Source: domain.Source{
						ID:      1,
						Name:    "テスト",
						Kind:    domain.SourceKindRSS,
						Enabled: true,
					},
					Description: "https://example.com/feed.xml",
				}},
			},
		},
		{
			name: "source_form.html",
			data: sourceFormPage{
				Base: base,
				Form: sourceForm{Kind: domain.SourceKindRSS},
			},
		},
		{
			name: "articles.html",
			data: articleListPage{
				Base: base,
			},
		},
		{
			name: "article.html",
			data: articleDetailPage{
				Base: base,
			},
		},
		{
			name: "runs.html",
			data: runListPage{Base: base},
		},
		{
			name: "error.html",
			data: struct{ Base basePage }{Base: base},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if err := server.template.ExecuteTemplate(io.Discard, testCase.name, testCase.data); err != nil {
				t.Errorf("ExecuteTemplate(%q) error = %v", testCase.name, err)
			}
		})
	}
}

// ----------------------------------------

/*
TestAdminDoesNotRequireAuthentication は、閉域ネットワーク向け管理画面が認証なしで開けることを検証します。

機能:
  - 管理画面ルートへの未認証リクエストを取得対象一覧へ転送することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestAdminDoesNotRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Errorf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusSeeOther)
	}
	if recorder.Header().Get("Location") != "/admin/sources" {
		t.Errorf("Location = %q, 期待値は %q", recorder.Header().Get("Location"), "/admin/sources")
	}
}

// ----------------------------------------

/*
TestRootRedirectsToSources は、トップページから取得対象一覧へ遷移できることを検証します。

機能:
  - ルートURLへのリクエストが管理画面の取得対象一覧へ転送されることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestRootRedirectsToSources(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Errorf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusSeeOther)
	}
	if recorder.Header().Get("Location") != "/admin/sources" {
		t.Errorf("Location = %q, 期待値は %q", recorder.Header().Get("Location"), "/admin/sources")
	}
}

// ----------------------------------------

/*
TestCSRFMiddlewareRejectsMissingToken は、トークンなしの状態変更を拒否することを検証します。

機能:
  - CSRFトークンを持たない取得対象作成リクエストへ403を返すことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestCSRFMiddlewareRejectsMissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/sources",
		strings.NewReader("name=不正な送信"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusForbidden)
	}
}

// ----------------------------------------

/*
TestSecurityHeadersOnStaticAsset は、静的配信にもセキュリティヘッダーが付くことを検証します。

機能:
  - CSS配信の成功、ダークテーマ、Grid.js用テーマ、X-Content-Type-Options・Content-Security-Policyを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestSecurityHeadersOnStaticAsset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Errorf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusOK)
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options ヘッダーが正しくありません")
	}
	if recorder.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf(
			"Cache-Control = %q, 期待値はno-cache",
			recorder.Header().Get("Cache-Control"),
		)
	}
	if !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("Content-Security-Policy ヘッダーが正しくありません")
	}
	if !strings.Contains(recorder.Body.String(), "color-scheme: dark") {
		t.Error("管理画面CSSがダークテーマを既定にしていません")
	}
	if !strings.Contains(
		recorder.Body.String(),
		".article-grid-container .gridjs-footer",
	) {
		t.Error("記事一覧のGrid.js用ダークテーマを確認できません")
	}
}

// ----------------------------------------

/*
TestSourceFormJavaScriptAsset は、種類別フォーム切替JavaScriptをローカル配信できることを検証します。

機能:
  - 埋め込みJavaScriptの状態コード、Content-Type、主要処理を確認する
  - CSPが同一オリジンのスクリプトだけを許可することを確認する

引数:
  - t *testing.T: 静的ファイル配信を検証するテスト状態

返り値:
  - なし
*/
func TestSourceFormJavaScriptAsset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/static/source-form.js", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/javascript") {
		t.Errorf("Content-Type = %q, JavaScriptを期待", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "updateConnectorFields") {
		t.Error("フォーム切替JavaScriptの内容を確認できません")
	}
	if !strings.Contains(
		recorder.Header().Get("Content-Security-Policy"),
		"script-src 'self'",
	) {
		t.Error("Content-Security-PolicyがローカルJavaScriptを制限していません")
	}
}

// ----------------------------------------

/*
TestLocalBulmaAsset は、web/libへ配置したBulmaを管理画面から配信できることを検証します。

機能:
  - CDNを使用せず埋め込み済みのBulma CSSへアクセスできることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLocalBulmaAsset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/lib/bulma/bulma.min.css", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 期待値は %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), ".button") {
		t.Error("Bulma CSSの内容を確認できません")
	}
}

// ----------------------------------------

/*
TestArticleJavaScriptAssets は、記事API画面のローカルJavaScript資産を検証します。

機能:
  - web/libのAxios、Grid.jsと埋め込み済み記事画面JavaScriptへアクセスできることを確認する
  - 一覧がGrid.jsの10件ページングと安全な仮想DOMを利用することを確認する
  - 記事詳細が本文分割APIと安全なTextNode追記を利用することを確認する
  - 更新対象の記事画面JavaScriptが長期キャッシュされないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestArticleJavaScriptAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server, err := New(nil, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	testCases := []struct {
		name            string
		path            string
		wantValues      []string
		forbiddenValues []string
		wantCache       string
	}{
		{
			name:       "Axios",
			path:       "/lib/axios/axios.min.js",
			wantValues: []string{"axios"},
		},
		{
			name:       "Grid.js",
			path:       "/lib/gridjs/gridjs.umd.js",
			wantValues: []string{"gridjs", "Grid"},
		},
		{
			name: "記事画面",
			path: "/static/articles.js",
			wantValues: []string{
				"window.axios.get",
				"new window.gridjs.Grid",
				"window.gridjs.h",
				"#article-keyword-1",
				"#article-keyword-5",
				"new Date(normalizedFromValue) > new Date(normalizedToValue)",
				`params.set("date_from", normalizedFromValue)`,
				`params.set("date_to", normalizedToValue)`,
				"limit: 10",
				"summary: false",
				"updateConfig({ data: gridData }).forceRender()",
				"/api/articles",
				"body_offset",
				"textContent",
				"appendChild(document.createTextNode(bodyChunk))",
			},
			forbiddenValues: []string{
				"body.textContent +=",
				"articleListState.nextCursor",
				`querySelector("#article-load-more")`,
				"body_excerpt",
				"最新の記事を取得しています",
				"続きがあります",
				"開始日時は終了日時以前にしてください",
			},
			wantCache: "no-cache",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("状態コード = %d, 期待値は200", recorder.Code)
			}
			if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(
				contentType,
				"javascript",
			) {
				t.Errorf("Content-Type = %q, JavaScriptを期待", contentType)
			}
			if testCase.wantCache != "" &&
				recorder.Header().Get("Cache-Control") != testCase.wantCache {
				t.Errorf(
					"Cache-Control = %q, 期待値は%q",
					recorder.Header().Get("Cache-Control"),
					testCase.wantCache,
				)
			}
			for _, value := range testCase.wantValues {
				if !strings.Contains(recorder.Body.String(), value) {
					t.Errorf("%s に %q がありません", testCase.path, value)
				}
			}
			for _, value := range testCase.forbiddenValues {
				if strings.Contains(recorder.Body.String(), value) {
					t.Errorf("%s に不要な %q があります", testCase.path, value)
				}
			}
			if !strings.Contains(
				recorder.Header().Get("Content-Security-Policy"),
				"script-src 'self'",
			) {
				t.Error("CSPが同一オリジンJavaScriptに制限されていません")
			}
		})
	}
}
