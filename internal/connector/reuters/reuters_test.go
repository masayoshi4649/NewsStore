package reuters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
)

/*
TestResolveCanonicalURL は、相対Reuters記事URLが取得先ホストの絶対URLになることを検証します。

機能:
  - Reutersの相対canonical URLを登録済みベースURLから解決する挙動を確認する

引数:
  - test *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestResolveCanonicalURL(test *testing.T) {
	test.Parallel()

	resolved, err := resolveCanonicalURL(
		"https://jp.reuters.com",
		"/world/example-article/",
	)
	if err != nil {
		test.Fatalf("resolveCanonicalURL() error = %v", err)
	}
	if resolved != "https://jp.reuters.com/world/example-article/" {
		test.Errorf(
			"resolveCanonicalURL() = %q, 期待値は %q",
			resolved,
			"https://jp.reuters.com/world/example-article/",
		)
	}
}

// ----------------------------------------

/*
TestTargetsAndFixedEndpoint は、Reuters取得対象カタログと本番固定URLを検証します。

機能:
  - 参照実装で確認した主要セクションがホワイトリストへ登録されていることを確認する
  - Targetsの返り値を変更しても内部定義へ影響しないことを確認する
  - NewがReuters日本語版の固定ベースURLを使用することを確認する

引数:
  - test *testing.T: Reuters定義を検証するテスト状態

返り値:
  - なし
*/
func TestTargetsAndFixedEndpoint(test *testing.T) {
	test.Parallel()

	targets := Targets()
	if len(targets) != 8 {
		test.Fatalf("取得対象数が一致しません: got=%d want=8", len(targets))
	}
	economy, ok := FindTarget(DefaultTargetID)
	if !ok || economy.SectionID != "/economy/" {
		test.Fatalf("経済取得対象が不正です: got=%#v exists=%t", economy, ok)
	}
	japan, ok := FindTargetBySectionID(japanSectionID)
	if !ok || japan.SectionID != japanSectionID {
		test.Fatalf("現行日本セクションをsection_idから検索できません: got=%#v", japan)
	}
	legacyJapan, ok := FindTargetBySectionID(legacyJapanSectionID)
	if !ok || legacyJapan.SectionID != japanSectionID {
		test.Fatalf("旧日本セクションを現行section_idへ変換できません: got=%#v", legacyJapan)
	}
	legacySettings, err := loadSettings(
		json.RawMessage(`{"section_id":"` + legacyJapanSectionID + `"}`),
	)
	if err != nil {
		test.Fatalf("旧日本セクション設定を読み込めません: %v", err)
	}
	if legacySettings.SectionID != japanSectionID {
		test.Fatalf(
			"旧日本セクション設定が現行値へ変換されません: got=%q want=%q",
			legacySettings.SectionID,
			japanSectionID,
		)
	}

	targets[0].Label = "変更"
	freshTargets := Targets()
	if freshTargets[0].Label == "変更" {
		test.Error("Targetsの返り値から内部定義が変更されました")
	}

	connector := New(nil, "", 0)
	if connector.endpointURL != EndpointURL {
		test.Errorf("固定URL = %q, 期待値は %q", connector.endpointURL, EndpointURL)
	}
}

// ----------------------------------------

/*
TestCompactRawPayload は、大きな未加工JSONがハッシュ付きメタデータへ置換されることを検証します。

機能:
  - 保存上限を超える原データが有効な切り詰めメタデータになることを確認する

引数:
  - test *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestCompactRawPayload(test *testing.T) {
	test.Parallel()

	rawPayload := json.RawMessage(
		`{"content":"` + strings.Repeat("a", maxRawPayloadBytes) + `"}`,
	)
	compacted := compactRawPayload(rawPayload)
	if !json.Valid(compacted) {
		test.Fatalf("compactRawPayload()が不正なJSONを返しました: %q", compacted)
	}
	if !strings.Contains(string(compacted), `"truncated":true`) ||
		!strings.Contains(string(compacted), `"sha256"`) {
		test.Errorf("compactRawPayload() = %s, 切り詰め情報を期待", compacted)
	}
}

// ----------------------------------------

// scriptedResponse は、テスト用HTTPサーバーが順番に返すレスポンスを表します。
type scriptedResponse struct {
	StatusCode int
	Body       string
}

// requestSnapshot は、テスト用HTTPサーバーが受け取ったリクエストの検証用コピーです。
type requestSnapshot struct {
	Method    string
	Path      string
	RawQuery  string
	Accept    string
	UserAgent string
}

// scriptedHandler は、レスポンスを順番に返しながら受信リクエストを記録します。
type scriptedHandler struct {
	test                     *testing.T
	mutex                    sync.Mutex
	responses                []scriptedResponse
	requests                 []requestSnapshot
	cancel                   context.CancelFunc
	cancelAfterResponseIndex int
}

// fakeRevisionChecker は、保存済みリビジョン判定を固定値で返します。
type fakeRevisionChecker struct {
	exists bool
	calls  int
}

/*
TestFetchSkipsStoredRevisionDetail は、保存済みReutersリビジョンの詳細取得を省略することを検証します。

機能:
  - 一覧のIDとリビジョンが保存済みの場合に詳細APIを呼ばないことを確認する
  - Source内のURLではなくコネクタへ定義された固定URLを使用することを確認する

引数:
  - test *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestFetchSkipsStoredRevisionDetail(test *testing.T) {
	test.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"result":{"articles":[{"id":"known","revision_id":"revision-1"}]}}`,
		))
	}))
	defer server.Close()

	checker := &fakeRevisionChecker{exists: true}
	reutersConnector := newConnector(server.URL, server.Client(), "テスト", 1024*1024)
	reutersConnector.SetRevisionChecker(checker)
	result, err := reutersConnector.Fetch(context.Background(), domain.Source{
		ID:          10,
		EndpointURL: "https://untrusted.example",
		MaxItems:    1,
		Settings:    json.RawMessage(`{}`),
	})
	if err != nil {
		test.Fatalf("Fetch() error = %v", err)
	}
	if result.Skipped != 1 || len(result.Articles) != 0 {
		test.Errorf(
			"Fetch() = skipped:%d articles:%d, 期待値は 1,0",
			result.Skipped,
			len(result.Articles),
		)
	}
	if requestCount != 1 {
		test.Errorf("HTTPリクエスト数 = %d, 一覧だけの1件を期待", requestCount)
	}
	if checker.calls != 1 {
		test.Errorf("存在確認回数 = %d, 期待値は1", checker.calls)
	}
}

// ----------------------------------------

/*
ArticleRevisionExists は、テストで設定した保存済み判定を返します。

機能:
  - 呼び出し回数を記録して固定の存在判定を返す

引数:
  - ctx context.Context: 未使用の確認コンテキスト
  - sourceID int64: 未使用の取得対象ID
  - externalID string: 未使用の記事ID
  - revisionID string: 未使用のリビジョンID

返り値:
  - bool: テストで設定した存在判定
  - error: 常にnil
*/
func (checker *fakeRevisionChecker) ArticleRevisionExists(
	_ context.Context,
	_ int64,
	_ string,
	_ string,
) (bool, error) {
	checker.calls++
	return checker.exists, nil
}

// ----------------------------------------

// ServeHTTP は、受信リクエストを記録して対応するテストレスポンスを返します。
//
// 機能:
//   - HTTPメソッド、URL、主要ヘッダーを検証用に記録する
//   - 登録順にステータスコードと本文を返す
//   - 登録済みレスポンスを使い切った場合はテストエラーと500を返す
//
// 引数:
//   - responseWriter (http.ResponseWriter): テストレスポンスの書き込み先
//   - request (*http.Request): Reutersコネクタから受け取ったリクエスト
//
// 返り値:
//   - なし
func (handler *scriptedHandler) ServeHTTP(
	responseWriter http.ResponseWriter,
	request *http.Request,
) {
	handler.mutex.Lock()
	responseIndex := len(handler.requests)
	handler.requests = append(handler.requests, requestSnapshot{
		Method:    request.Method,
		Path:      request.URL.Path,
		RawQuery:  request.URL.RawQuery,
		Accept:    request.Header.Get("Accept"),
		UserAgent: request.Header.Get("User-Agent"),
	})

	if responseIndex >= len(handler.responses) {
		handler.mutex.Unlock()
		handler.test.Errorf("未定義のHTTPリクエストを受信しました: index=%d", responseIndex)
		responseWriter.WriteHeader(http.StatusInternalServerError)
		return
	}

	currentResponse := handler.responses[responseIndex]
	handler.mutex.Unlock()

	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(currentResponse.StatusCode)
	if _, err := responseWriter.Write([]byte(currentResponse.Body)); err != nil {
		handler.test.Errorf("テストレスポンス本文の書き込みに失敗しました: %v", err)
	}
	if handler.cancel != nil && responseIndex == handler.cancelAfterResponseIndex {
		handler.cancel()
	}
}

// ----------------------------------------

// snapshots は、記録済みリクエストの安全なコピーを返します。
//
// 機能:
//   - ミューテックスで並行アクセスを保護する
//   - 呼び出し元から内部スライスを変更できないようコピーする
//
// 引数:
//   - なし
//
// 返り値:
//   - []requestSnapshot: 記録済みリクエストのコピー
func (handler *scriptedHandler) snapshots() []requestSnapshot {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()

	return append([]requestSnapshot(nil), handler.requests...)
}

// ----------------------------------------

// newScriptedServer は、指定したレスポンスを順番に返すテスト用HTTPサーバーを作成します。
//
// 機能:
//   - scriptedHandlerとhttptest.Serverを初期化する
//   - テスト終了時にサーバーが自動で終了するよう登録する
//
// 引数:
//   - test (*testing.T): エラー報告とクリーンアップ登録に使用するテスト
//   - responses ([]scriptedResponse): リクエスト順に返すレスポンス
//
// 返り値:
//   - *httptest.Server: 起動済みのテスト用HTTPサーバー
//   - *scriptedHandler: 受信リクエストを保持するハンドラー
func newScriptedServer(
	test *testing.T,
	responses []scriptedResponse,
) (*httptest.Server, *scriptedHandler) {
	test.Helper()

	handler := &scriptedHandler{
		test:      test,
		responses: responses,
		requests:  make([]requestSnapshot, 0, len(responses)),
	}
	server := httptest.NewServer(handler)
	test.Cleanup(server.Close)

	return server, handler
}

// ----------------------------------------

// decodeQuery は、記録したURLのqueryパラメータからReuters用JSONを解析します。
//
// 機能:
//   - URLクエリ文字列を解析する
//   - queryパラメータが1個だけ存在することを検証する
//   - queryのJSONを指定された型へ変換する
//
// 引数:
//   - test (*testing.T): 検証失敗を報告するテスト
//   - rawQuery (string): HTTPリクエストの未加工クエリ文字列
//
// 返り値:
//   - queryType: query JSONを解析した値
func decodeQuery[queryType any](test *testing.T, rawQuery string) queryType {
	test.Helper()

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		test.Fatalf("URLクエリの解析に失敗しました: %v", err)
	}
	if len(values) != 1 || len(values["query"]) != 1 {
		test.Fatalf("queryパラメータが1個ではありません: %#v", values)
	}

	var decoded queryType
	if err := json.Unmarshal([]byte(values.Get("query")), &decoded); err != nil {
		test.Fatalf("query JSONの解析に失敗しました: %v", err)
	}

	return decoded
}

// ----------------------------------------

// testSource は、Reutersコネクタテスト用の取得元を作成します。
//
// 機能:
//   - HTTPサーバーURL、取得元ID、最大取得件数を設定する
//   - 呼び出し元からReuters固有JSON設定を指定できるようにする
//
// 引数:
//   - endpointURL (string): テスト用HTTPサーバーのベースURL
//   - maxItems (int): 一覧と詳細で取得する最大記事件数
//   - rawSettings (string): Reuters固有設定のJSON文字列
//
// 返り値:
//   - domain.Source: Reutersコネクタへ渡すテスト用取得元
func testSource(endpointURL string, maxItems int, rawSettings string) domain.Source {
	return domain.Source{
		ID:          42,
		Name:        "ロイター",
		Kind:        domain.SourceKindReuters,
		EndpointURL: endpointURL,
		Enabled:     true,
		MaxItems:    maxItems,
		Settings:    json.RawMessage(rawSettings),
	}
}

// ----------------------------------------

// TestFetchBuildsRequestsAndConvertsArticle は、選択セクションを使う2段階取得と記事変換を検証します。
//
// 機能:
//   - 一覧と詳細のURL、query JSON、Accept、User-Agentを検証する
//   - datelineとparagraphだけが本文へ変換されることを検証する
//   - 日時、識別子、見出し、未加工JSONのドメイン変換を検証する
//
// 引数:
//   - test (*testing.T): Reutersコネクタの動作を検証するテスト
//
// 返り値:
//   - なし
func TestFetchBuildsRequestsAndConvertsArticle(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"articles": [
						{
							"id": "summary-1",
							"revision_id": "summary-revision",
							"title": "一覧の見出し",
							"canonical_url": "/article/summary-1"
						}
					]
				}
			}`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"statusCode": 200,
				"result": {
					"id": "article-1",
					"revision_id": "revision-2",
					"title": "  詳細の見出し  ",
					"canonical_url": "https://example.test/article-1",
					"published_time": "2026-07-25T12:34:56.123456789+09:00",
					"updated_time": "2026-07-25T13:00:00+09:00",
					"dateline": [" 東京　２５日　ロイター ", "  "],
					"content_elements": [
						{"type": "paragraph", "content": " 第1段落 "},
						{"type": "image", "content": "画像説明"},
						{"type": "paragraph", "content": ""},
						{"type": "paragraph", "content": "第2段落"}
					]
				}
			}`,
		},
	}
	server, handler := newScriptedServer(test, responses)
	source := testSource(
		server.URL+"/custom-base/",
		3,
		`{"section_id":"/world/","website":"custom-website","offset":7}`,
	)

	result, err := newConnector(source.EndpointURL, server.Client(), "NewsStore-Test/2.0", 64*1024).
		Fetch(context.Background(), source)
	if err != nil {
		test.Fatalf("Fetchが失敗しました: %v", err)
	}

	requests := handler.snapshots()
	if len(requests) != 2 {
		test.Fatalf("リクエスト数が一致しません: got=%d want=2", len(requests))
	}

	if requests[0].Method != http.MethodGet {
		test.Errorf("一覧HTTPメソッドが一致しません: got=%q", requests[0].Method)
	}
	if requests[0].Path != "/custom-base/pf/api/v3/content/fetch/"+sectionFetchSource {
		test.Errorf("一覧URLパスが一致しません: got=%q", requests[0].Path)
	}
	if requests[1].Path != "/custom-base/pf/api/v3/content/fetch/"+articleFetchSource {
		test.Errorf("詳細URLパスが一致しません: got=%q", requests[1].Path)
	}
	for requestIndex, request := range requests {
		if request.Accept != acceptHeader {
			test.Errorf(
				"Acceptヘッダーが一致しません: index=%d got=%q want=%q",
				requestIndex,
				request.Accept,
				acceptHeader,
			)
		}
		if request.UserAgent != "NewsStore-Test/2.0" {
			test.Errorf(
				"User-Agentが一致しません: index=%d got=%q",
				requestIndex,
				request.UserAgent,
			)
		}
	}

	listQuery := decodeQuery[sectionQuery](test, requests[0].RawQuery)
	expectedListQuery := sectionQuery{
		ArcSite:   WebsiteID,
		FetchType: "collection",
		Offset:    0,
		SectionID: "/world/",
		Size:      3,
		Website:   WebsiteID,
	}
	if listQuery != expectedListQuery {
		test.Errorf("一覧queryが一致しません: got=%#v want=%#v", listQuery, expectedListQuery)
	}

	detailQuery := decodeQuery[articleQuery](test, requests[1].RawQuery)
	expectedDetailQuery := articleQuery{
		ID:      "summary-1",
		Website: WebsiteID,
	}
	if detailQuery != expectedDetailQuery {
		test.Errorf("詳細queryが一致しません: got=%#v want=%#v", detailQuery, expectedDetailQuery)
	}

	if len(result.Warnings) != 0 {
		test.Errorf("警告が発生しました: %#v", result.Warnings)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("記事数が一致しません: got=%d want=1", len(result.Articles))
	}

	article := result.Articles[0]
	if article.SourceID != 42 {
		test.Errorf("SourceIDが一致しません: got=%d want=42", article.SourceID)
	}
	if article.Provider != reutersProvider {
		test.Errorf("Providerが一致しません: got=%q want=%q", article.Provider, reutersProvider)
	}
	if article.ExternalID != "article-1" {
		test.Errorf("ExternalIDが一致しません: got=%q", article.ExternalID)
	}
	if article.RevisionID != "revision-2" {
		test.Errorf("RevisionIDが一致しません: got=%q", article.RevisionID)
	}
	if article.CanonicalURL != "https://example.test/article-1" {
		test.Errorf("CanonicalURLが一致しません: got=%q", article.CanonicalURL)
	}
	if article.Headline != "詳細の見出し" {
		test.Errorf("Headlineが一致しません: got=%q", article.Headline)
	}
	expectedBody := "東京　２５日　ロイター\n第1段落\n第2段落"
	if article.BodyText != expectedBody {
		test.Errorf("BodyTextが一致しません: got=%q want=%q", article.BodyText, expectedBody)
	}

	expectedPublishedAt, parseErr := time.Parse(
		time.RFC3339Nano,
		"2026-07-25T12:34:56.123456789+09:00",
	)
	if parseErr != nil {
		test.Fatalf("テスト日時の解析に失敗しました: %v", parseErr)
	}
	if article.PublishedAt == nil || !article.PublishedAt.Equal(expectedPublishedAt) {
		test.Errorf("PublishedAtが一致しません: got=%v want=%v", article.PublishedAt, expectedPublishedAt)
	}

	expectedUpdatedAt, parseErr := time.Parse(
		time.RFC3339Nano,
		"2026-07-25T13:00:00+09:00",
	)
	if parseErr != nil {
		test.Fatalf("テスト日時の解析に失敗しました: %v", parseErr)
	}
	if article.UpdatedAt == nil || !article.UpdatedAt.Equal(expectedUpdatedAt) {
		test.Errorf("UpdatedAtが一致しません: got=%v want=%v", article.UpdatedAt, expectedUpdatedAt)
	}

	if !json.Valid(article.RawPayload) {
		test.Errorf("RawPayloadが有効なJSONではありません: %q", string(article.RawPayload))
	}
	if !strings.Contains(string(article.RawPayload), `"statusCode": 200`) {
		test.Errorf("RawPayloadに未加工レスポンス全体が保存されていません: %q", string(article.RawPayload))
	}
}

// ----------------------------------------

// TestFetchUsesDefaultSettingsAndCanonicalURL は、既定設定とcanonical URLによる詳細取得を検証します。
//
// 機能:
//   - 空のSettingsからsection_id、website、offsetの既定値が使われることを検証する
//   - 一覧記事にIDがない場合はwebsite_urlで詳細取得することを検証する
//   - updated_timeが空の場合はUpdatedAtがnilになることを検証する
//
// 引数:
//   - test (*testing.T): Reutersコネクタの動作を検証するテスト
//
// 返り値:
//   - なし
func TestFetchUsesDefaultSettingsAndCanonicalURL(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body:       `{"result":{"articles":[{"canonical_url":"/article/by-url"}]}}`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"id": "url-article",
					"revision_id": "revision-1",
					"title": "URLで取得した記事",
					"canonical_url": "/article/by-url",
					"published_time": "2026-07-25T01:02:03Z",
					"updated_time": "",
					"dateline": [],
					"content_elements": [{"type":"paragraph","content":"本文"}]
				}
			}`,
		},
	}
	server, handler := newScriptedServer(test, responses)
	source := testSource(server.URL, 1, "")

	result, err := newConnector(server.URL, server.Client(), "", 64*1024).
		Fetch(context.Background(), source)
	if err != nil {
		test.Fatalf("Fetchが失敗しました: %v", err)
	}

	requests := handler.snapshots()
	if len(requests) != 2 {
		test.Fatalf("リクエスト数が一致しません: got=%d want=2", len(requests))
	}

	listQuery := decodeQuery[sectionQuery](test, requests[0].RawQuery)
	expectedListQuery := sectionQuery{
		ArcSite:   defaultWebsite,
		FetchType: "collection",
		Offset:    0,
		SectionID: defaultSectionID,
		Size:      1,
		Website:   defaultWebsite,
	}
	if listQuery != expectedListQuery {
		test.Errorf("既定の一覧queryが一致しません: got=%#v want=%#v", listQuery, expectedListQuery)
	}

	detailQuery := decodeQuery[articleQuery](test, requests[1].RawQuery)
	expectedDetailQuery := articleQuery{
		WebsiteURL: "/article/by-url",
		Website:    defaultWebsite,
	}
	if detailQuery != expectedDetailQuery {
		test.Errorf("URL指定の詳細queryが一致しません: got=%#v want=%#v", detailQuery, expectedDetailQuery)
	}
	if requests[0].UserAgent != defaultUserAgent {
		test.Errorf(
			"既定User-Agentが一致しません: got=%q want=%q",
			requests[0].UserAgent,
			defaultUserAgent,
		)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("記事数が一致しません: got=%d want=1", len(result.Articles))
	}
	if result.Articles[0].UpdatedAt != nil {
		test.Errorf("空のupdated_timeがnilになっていません: %v", result.Articles[0].UpdatedAt)
	}
}

// ----------------------------------------

// TestFetchReturnsBoundedNonOKBody は、一覧APIの非200エラー本文が上限内へ切り詰められることを検証します。
//
// 機能:
//   - 非200ステータスが一覧取得全体のエラーになることを検証する
//   - エラーへ最大バイト数までのレスポンス本文が含まれることを検証する
//   - サイズ上限を超えた本文がエラーへ漏れないことを検証する
//
// 引数:
//   - test (*testing.T): Reutersコネクタのエラー処理を検証するテスト
//
// 返り値:
//   - なし
func TestFetchReturnsBoundedNonOKBody(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusBadGateway,
			Body:       strings.Repeat("1", int(maxErrorBodyPreview)) + "SECRET",
		},
	}
	server, _ := newScriptedServer(test, responses)
	source := testSource(server.URL, 1, "")

	_, err := newConnector(server.URL, server.Client(), "test-agent", 8).
		Fetch(context.Background(), source)
	if err == nil {
		test.Fatal("非200レスポンスでエラーが返されませんでした")
	}

	errorText := err.Error()
	if !strings.Contains(errorText, "status=502") {
		test.Errorf("エラーにHTTPステータスが含まれません: %q", errorText)
	}
	if !strings.Contains(errorText, "11111111") {
		test.Errorf("エラーに上限内の本文が含まれません: %q", errorText)
	}
	if strings.Contains(errorText, "SECRET") {
		test.Errorf("エラーにサイズ上限外の本文が含まれています: %q", errorText)
	}
	if !strings.Contains(errorText, "プレビュー上限で省略") {
		test.Errorf("エラーに本文省略の情報が含まれません: %q", errorText)
	}
}

// ----------------------------------------

// TestFetchRejectsMalformedSectionJSON は、一覧APIの壊れたJSONを全体エラーとして扱うことを検証します。
//
// 機能:
//   - HTTP 200でも一覧JSONが壊れていればエラーになることを検証する
//   - エラーにJSON解析失敗の情報が含まれることを検証する
//
// 引数:
//   - test (*testing.T): ReutersコネクタのJSONエラー処理を検証するテスト
//
// 返り値:
//   - なし
func TestFetchRejectsMalformedSectionJSON(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body:       `{"result":{"articles":[`,
		},
	}
	server, _ := newScriptedServer(test, responses)
	source := testSource(server.URL, 1, "")

	_, err := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), source)
	if err == nil {
		test.Fatal("壊れた一覧JSONでエラーが返されませんでした")
	}
	if !strings.Contains(err.Error(), "JSON解析") {
		test.Errorf("JSON解析エラーの説明が含まれません: %v", err)
	}
}

// ----------------------------------------

// TestFetchContinuesAfterIndividualDetailFailures は、個別詳細取得失敗後も残りの記事を取得することを検証します。
//
// 機能:
//   - 詳細APIの非200を警告へ変換する
//   - 詳細APIの壊れたJSONを警告へ変換する
//   - 失敗記事の後にある正常記事を結果へ追加する
//
// 引数:
//   - test (*testing.T): Reutersコネクタの継続処理を検証するテスト
//
// 返り値:
//   - なし
func TestFetchContinuesAfterIndividualDetailFailures(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"articles": [
						{"id":"article-error"},
						{"id":"article-json"},
						{"id":"article-success"}
					]
				}
			}`,
		},
		{
			StatusCode: http.StatusServiceUnavailable,
			Body:       "detail unavailable",
		},
		{
			StatusCode: http.StatusOK,
			Body:       `{"result":`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"id": "article-success",
					"revision_id": "revision-success",
					"title": "取得成功",
					"canonical_url": "/article-success",
					"published_time": "2026-07-25T10:00:00Z",
					"content_elements": [{"type":"paragraph","content":"本文"}]
				}
			}`,
		},
	}
	server, handler := newScriptedServer(test, responses)
	source := testSource(server.URL, 3, "")

	result, err := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), source)
	if err != nil {
		test.Fatalf("個別エラーが全体エラーになりました: %v", err)
	}

	if len(handler.snapshots()) != 4 {
		test.Errorf("全記事の詳細取得が試行されていません: got=%d want=4", len(handler.snapshots()))
	}
	if len(result.Warnings) != 2 {
		test.Fatalf("警告数が一致しません: got=%d want=2 warnings=%#v", len(result.Warnings), result.Warnings)
	}
	if !strings.Contains(result.Warnings[0], "status=503") {
		test.Errorf("非200警告にステータスが含まれません: %q", result.Warnings[0])
	}
	if !strings.Contains(result.Warnings[1], "JSON解析") {
		test.Errorf("壊れたJSONの警告に解析情報が含まれません: %q", result.Warnings[1])
	}
	if len(result.Articles) != 1 {
		test.Fatalf("正常記事数が一致しません: got=%d want=1", len(result.Articles))
	}
	if result.Articles[0].ExternalID != "article-success" {
		test.Errorf("正常記事が結果へ格納されていません: %#v", result.Articles[0])
	}
}

// ----------------------------------------

// TestFetchRejectsOversizedSuccessfulResponse は、正常ステータスのレスポンスサイズ上限を検証します。
//
// 機能:
//   - HTTP 200でも一覧本文が上限を超えれば全体エラーになることを検証する
//   - エラーへ設定した最大レスポンスサイズが含まれることを検証する
//
// 引数:
//   - test (*testing.T): Reutersコネクタのサイズ制限を検証するテスト
//
// 返り値:
//   - なし
func TestFetchRejectsOversizedSuccessfulResponse(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body:       `{"result":{"articles":[]}}`,
		},
	}
	server, _ := newScriptedServer(test, responses)
	source := testSource(server.URL, 1, "")

	_, err := newConnector(server.URL, server.Client(), "test-agent", 10).
		Fetch(context.Background(), source)
	if err == nil {
		test.Fatal("サイズ上限超過でエラーが返されませんでした")
	}
	if !strings.Contains(err.Error(), "max_response_bytes=10") {
		test.Errorf("サイズ上限エラーに設定値が含まれません: %v", err)
	}
}

// ----------------------------------------

// TestFetchWarnsForInvalidArticleTimes は、記事詳細の必須日時と任意日時の検証を確認します。
//
// 機能:
//   - published_timeがない詳細を警告へ変換する
//   - updated_timeが不正な詳細を警告へ変換する
//   - 日時が正常な後続記事は取得結果へ追加する
//
// 引数:
//   - test (*testing.T): Reuters記事日時の変換を検証するテスト
//
// 返り値:
//   - なし
func TestFetchWarnsForInvalidArticleTimes(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"articles": [
						{"id":"missing-published"},
						{"id":"invalid-updated"},
						{"id":"valid-time"}
					]
				}
			}`,
		},
		{
			StatusCode: http.StatusOK,
			Body:       `{"result":{"id":"missing-published","title":"日時なし"}}`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"id":"invalid-updated",
					"title":"更新日時不正",
					"published_time":"2026-07-25T00:00:00Z",
					"updated_time":"not-a-time"
				}
			}`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"id":"valid-time",
					"revision_id":"valid-time-revision",
					"title":"正常",
					"canonical_url":"/valid-time",
					"published_time":"2026-07-25T00:00:00.123456789Z",
					"content_elements":[{"type":"paragraph","content":"本文"}]
				}
			}`,
		},
	}
	server, _ := newScriptedServer(test, responses)
	source := testSource(server.URL, 3, "")

	result, err := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), source)
	if err != nil {
		test.Fatalf("個別日時エラーが全体エラーになりました: %v", err)
	}
	if len(result.Warnings) != 2 {
		test.Errorf("日時警告数が一致しません: got=%d want=2", len(result.Warnings))
	}
	if len(result.Articles) != 1 || result.Articles[0].ExternalID != "valid-time" {
		test.Errorf("正常日時の記事が取得できませんでした: %#v", result.Articles)
	}
}

// ----------------------------------------

// TestFetchHonorsContextCancellation は、呼び出し元コンテキストのキャンセル伝播を検証します。
//
// 機能:
//   - キャンセル済みコンテキストでHTTP処理が中断されることを検証する
//   - ラップされたエラーからcontext.Canceledを判定できることを検証する
//
// 引数:
//   - test (*testing.T): Reutersコネクタのコンテキスト処理を検証するテスト
//
// 返り値:
//   - なし
func TestFetchHonorsContextCancellation(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body:       `{"result":{"articles":[]}}`,
		},
	}
	server, _ := newScriptedServer(test, responses)
	source := testSource(server.URL, 1, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(ctx, source)
	if err == nil {
		test.Fatal("キャンセル済みコンテキストでエラーが返されませんでした")
	}
	if !errors.Is(err, context.Canceled) {
		test.Errorf("context.Canceledを判定できません: %v", err)
	}
}

// ----------------------------------------

// TestFetchValidatesSettingsAndMaxItems は、HTTP実行前の入力検証を確認します。
//
// 機能:
//   - 壊れたSettings JSONを設定エラーとして扱う
//   - 未対応のsection_idを設定エラーとして扱う
//   - 範囲外のMaxItemsを入力エラーとして扱う
//   - 入力エラー時にHTTPリクエストを実行しない
//
// 引数:
//   - test (*testing.T): Reutersコネクタの入力検証を確認するテスト
//
// 返り値:
//   - なし
func TestFetchValidatesSettingsAndMaxItems(test *testing.T) {
	server, handler := newScriptedServer(test, nil)

	invalidSettingsSource := testSource(server.URL, 1, `{`)
	_, settingsErr := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), invalidSettingsSource)
	if settingsErr == nil || !strings.Contains(settingsErr.Error(), "settings") {
		test.Errorf("壊れたSettings JSONが適切に拒否されません: %v", settingsErr)
	}

	invalidSectionSource := testSource(server.URL, 1, `{"section_id":"/unsupported/"}`)
	_, sectionErr := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), invalidSectionSource)
	if sectionErr == nil || !strings.Contains(sectionErr.Error(), "section_id") {
		test.Errorf("未対応section_idが適切に拒否されません: %v", sectionErr)
	}

	invalidMaxItemsSource := testSource(server.URL, 0, "")
	_, maxItemsErr := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), invalidMaxItemsSource)
	if maxItemsErr == nil || !strings.Contains(maxItemsErr.Error(), "max_items") {
		test.Errorf("不正なMaxItemsが適切に拒否されません: %v", maxItemsErr)
	}

	tooLargeMaxItemsSource := testSource(server.URL, maxSourceItems+1, "")
	_, tooLargeMaxItemsErr := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), tooLargeMaxItemsSource)
	if tooLargeMaxItemsErr == nil || !strings.Contains(tooLargeMaxItemsErr.Error(), "max_items") {
		test.Errorf("上限超過のMaxItemsが適切に拒否されません: %v", tooLargeMaxItemsErr)
	}

	if len(handler.snapshots()) != 0 {
		test.Errorf("入力エラー時にHTTPリクエストが実行されました: %d", len(handler.snapshots()))
	}
}

// ----------------------------------------

// TestFetchLimitsReturnedSummariesToMaxItems は、一覧APIが過剰に返した記事をMaxItemsまでに制限することを検証します。
//
// 機能:
//   - 一覧レスポンスの記事数がMaxItemsを超えても詳細取得数を制限する
//   - 制限内の記事だけが結果へ追加されることを検証する
//
// 引数:
//   - test (*testing.T): Reutersコネクタの取得件数制限を検証するテスト
//
// 返り値:
//   - なし
func TestFetchLimitsReturnedSummariesToMaxItems(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"articles": [
						{"id":"first"},
						{"id":"second"}
					]
				}
			}`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"id":"first",
					"revision_id":"first-revision",
					"title":"1件目",
					"canonical_url":"/first",
					"published_time":"2026-07-25T00:00:00Z",
					"content_elements":[{"type":"paragraph","content":"本文"}]
				}
			}`,
		},
	}
	server, handler := newScriptedServer(test, responses)
	source := testSource(server.URL, 1, "")

	result, err := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(context.Background(), source)
	if err != nil {
		test.Fatalf("Fetchが失敗しました: %v", err)
	}
	if len(handler.snapshots()) != 2 {
		test.Errorf("MaxItemsを超える詳細取得が行われました: got=%d want=2", len(handler.snapshots()))
	}
	if len(result.Articles) != 1 || result.Articles[0].ExternalID != "first" {
		test.Errorf("MaxItems内の記事結果が一致しません: %#v", result.Articles)
	}
}

// ----------------------------------------

// TestBuildFetchURLRejectsUnsafeEndpoint は、安全でないベースURLが拒否されることを検証します。
//
// 機能:
//   - HTTP以外のschemeを持つベースURLを拒否する
//   - ユーザー情報を含むベースURLを拒否する
//
// 引数:
//   - test (*testing.T): Reuters API URLの入力検証を確認するテスト
//
// 返り値:
//   - なし
func TestBuildFetchURLRejectsUnsafeEndpoint(test *testing.T) {
	query := sectionQuery{
		ArcSite:   defaultWebsite,
		FetchType: "collection",
		SectionID: defaultSectionID,
		Size:      1,
		Website:   defaultWebsite,
	}

	_, schemeErr := buildFetchURL("ftp://example.test", sectionFetchSource, query)
	if schemeErr == nil || !strings.Contains(schemeErr.Error(), "scheme") {
		test.Errorf("HTTP以外のschemeが拒否されません: %v", schemeErr)
	}

	_, userInfoErr := buildFetchURL(
		"https://user:password@example.test",
		sectionFetchSource,
		query,
	)
	if userInfoErr == nil || !strings.Contains(userInfoErr.Error(), "ユーザー情報") {
		test.Errorf("ユーザー情報を含むURLが拒否されません: %v", userInfoErr)
	}
}

// ----------------------------------------

// TestFetchStopsWhenContextIsCanceledDuringDetails は、詳細取得中のキャンセルで即時終了することを検証します。
//
// 機能:
//   - 一覧取得後の詳細レスポンス処理中にコンテキストをキャンセルする
//   - キャンセルを個別警告へ変換せず全体エラーとして返す
//   - キャンセル後に残りの記事詳細を要求しない
//
// 引数:
//   - test (*testing.T): Reutersコネクタの中断処理を検証するテスト
//
// 返り値:
//   - なし
func TestFetchStopsWhenContextIsCanceledDuringDetails(test *testing.T) {
	responses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"articles": [
						{"id":"first"},
						{"id":"second"},
						{"id":"third"}
					]
				}
			}`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"result": {
					"id":"first",
					"revision_id":"first-revision",
					"title":"1件目",
					"canonical_url":"/first",
					"published_time":"2026-07-25T00:00:00Z",
					"content_elements":[{"type":"paragraph","content":"本文"}]
				}
			}`,
		},
	}
	server, handler := newScriptedServer(test, responses)
	source := testSource(server.URL, 3, "")
	ctx, cancel := context.WithCancel(context.Background())
	test.Cleanup(cancel)
	handler.cancel = cancel
	handler.cancelAfterResponseIndex = 1

	_, err := newConnector(server.URL, server.Client(), "test-agent", 64*1024).
		Fetch(ctx, source)
	if err == nil {
		test.Fatal("詳細取得中のキャンセルでエラーが返されませんでした")
	}
	if !errors.Is(err, context.Canceled) {
		test.Errorf("context.Canceledを判定できません: %v", err)
	}
	if len(handler.snapshots()) != 2 {
		test.Errorf(
			"キャンセル後も詳細リクエストが継続されました: got=%d want=2",
			len(handler.snapshots()),
		)
	}
}

// ----------------------------------------

// TestFetchValidatesAPIEnvelope は、HTTP 200内のAPIエラーとresult欠落を検証します。
//
// 機能:
//   - 一覧APIのresult欠落を全体エラーとして扱う
//   - 詳細APIの業務ステータスエラーを個別警告として扱う
//   - APIエラーを空の正常結果として誤認しない
//
// 引数:
//   - test (*testing.T): Reuters APIレスポンス構造の検証を確認するテスト
//
// 返り値:
//   - なし
func TestFetchValidatesAPIEnvelope(test *testing.T) {
	missingResultResponses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body:       `{"message":"result missing"}`,
		},
	}
	missingResultServer, _ := newScriptedServer(test, missingResultResponses)
	missingResultSource := testSource(missingResultServer.URL, 1, "")

	_, missingResultErr := newConnector(
		missingResultServer.URL,
		missingResultServer.Client(),
		"test-agent",
		64*1024,
	).
		Fetch(context.Background(), missingResultSource)
	if missingResultErr == nil || !strings.Contains(missingResultErr.Error(), "result") {
		test.Errorf("一覧APIのresult欠落が適切に拒否されません: %v", missingResultErr)
	}

	detailStatusResponses := []scriptedResponse{
		{
			StatusCode: http.StatusOK,
			Body:       `{"result":{"articles":[{"id":"business-error"}]}}`,
		},
		{
			StatusCode: http.StatusOK,
			Body: `{
				"statusCode": 500,
				"message": "upstream failure",
				"result": {
					"id":"business-error",
					"revision_id":"revision",
					"title":"業務エラー",
					"canonical_url":"/business-error",
					"published_time":"2026-07-25T00:00:00Z",
					"content_elements":[{"type":"paragraph","content":"本文"}]
				}
			}`,
		},
	}
	detailStatusServer, _ := newScriptedServer(test, detailStatusResponses)
	detailStatusSource := testSource(detailStatusServer.URL, 1, "")

	result, detailStatusErr := newConnector(
		detailStatusServer.URL,
		detailStatusServer.Client(),
		"test-agent",
		64*1024,
	).
		Fetch(context.Background(), detailStatusSource)
	if detailStatusErr != nil {
		test.Fatalf("個別API業務エラーが全体エラーになりました: %v", detailStatusErr)
	}
	if len(result.Articles) != 0 || len(result.Warnings) != 1 {
		test.Errorf(
			"個別API業務エラーの結果が一致しません: articles=%d warnings=%#v",
			len(result.Articles),
			result.Warnings,
		)
	}
	if len(result.Warnings) == 1 &&
		!strings.Contains(result.Warnings[0], "statusCode=500") {
		test.Errorf("警告にAPI業務ステータスが含まれません: %q", result.Warnings[0])
	}
}

// ----------------------------------------

// TestConvertArticleRejectsMissingRequiredFields は、Reuters記事の必須保存項目を検証します。
//
// 機能:
//   - ID、リビジョンID、canonical URL、見出し、本文の空値を拒否する
//   - 各エラーから不足した共通記事項目を識別できることを検証する
//
// 引数:
//   - test (*testing.T): Reuters記事変換の必須項目検証を確認するテスト
//
// 返り値:
//   - なし
func TestConvertArticleRejectsMissingRequiredFields(test *testing.T) {
	validDetail := articleDetail{
		ID:            "article",
		RevisionID:    "revision",
		Title:         "見出し",
		CanonicalURL:  "/article",
		PublishedTime: "2026-07-25T00:00:00Z",
		ContentElements: []contentElement{
			{Type: "paragraph", Content: "本文"},
		},
	}

	missingID := validDetail
	missingID.ID = ""
	missingRevision := validDetail
	missingRevision.RevisionID = ""
	missingCanonicalURL := validDetail
	missingCanonicalURL.CanonicalURL = ""
	missingHeadline := validDetail
	missingHeadline.Title = ""
	missingBody := validDetail
	missingBody.ContentElements = nil

	testCases := []struct {
		name          string
		detail        articleDetail
		expectedField string
	}{
		{name: "ID", detail: missingID, expectedField: "external_id"},
		{name: "リビジョンID", detail: missingRevision, expectedField: "revision_id"},
		{name: "canonical URL", detail: missingCanonicalURL, expectedField: "canonical_url"},
		{name: "見出し", detail: missingHeadline, expectedField: "headline"},
		{name: "本文", detail: missingBody, expectedField: "body_text"},
	}

	for _, testCase := range testCases {
		_, err := convertArticle(42, testCase.detail, json.RawMessage(`{}`))
		if err == nil {
			test.Errorf("%s欠落でエラーが返されませんでした", testCase.name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.expectedField) {
			test.Errorf(
				"%s欠落エラーに項目名が含まれません: %v",
				testCase.name,
				err,
			)
		}
	}
}
