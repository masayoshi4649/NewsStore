package fisco

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
)

// scriptedResponse は、HTTP クライアントが順番に返す応答です。
type scriptedResponse struct {
	status int
	body   string
	err    error
}

// scriptedClient は、外部通信をせず要求とキャンセルを検証するクライアントです。
type scriptedClient struct {
	responses []scriptedResponse
	requests  []*http.Request
	cancel    context.CancelFunc
	cancelAt  int
}

// Do は、要求を記録してあらかじめ用意した応答を返します。
//
// 機能:
//   - 指定した順番の要求でコンテキストをキャンセルできる
//
// 引数:
//   - request (*http.Request): コネクタから受け取る要求
//
// 返り値:
//   - *http.Response: 用意されたステータスと本文
//   - error: 用意された通信エラーまたは予定外の要求エラー
func (client *scriptedClient) Do(request *http.Request) (*http.Response, error) {
	index := len(client.requests)
	client.requests = append(client.requests, request)
	if client.cancel != nil && index == client.cancelAt {
		client.cancel()
	}
	if index >= len(client.responses) {
		return nil, errors.New("予定外の HTTP リクエストです")
	}
	response := client.responses[index]
	if response.err != nil {
		return nil, response.err
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
}

// ----------------------------------------

// TestFetchMapsFiscoFields は、一覧と詳細を指定どおり共通記事へ対応付けることを検証します。
//
// 機能:
//   - 固定 URL、limit、HTTP ヘッダーと過剰な一覧件数の制限を検証する
//   - 一覧の見出しと日本時間、詳細本文、公式記事 URL を検証する
//   - Source の URL や設定と API 内の URL を使用しないことを検証する
//
// 引数:
//   - test (*testing.T): テスト状態
//
// 返り値:
//   - なし
func TestFetchMapsFiscoFields(test *testing.T) {
	detailJSON := `{"news":{"newsId":"0009340020260908019","newsTitle":"詳細の見出し","newsDateTime":"20000101000000","newsReport":"  銘柄<6905>が上昇。\n続きの全文です。  ","url":"https://example.invalid/記事"},"newsCommon":[{"newsReport":"関連記事本文"}]}`
	client := &scriptedClient{responses: []scriptedResponse{
		{body: `{"data":[{"newsId":"0009340020260908019","newsTitle":" 一覧の見出し ","newsDateTime":"20260908173220","newsReport":"一覧の短い本文","url":"https://example.invalid/一覧"},{"newsId":"取得しない記事"}]}`},
		{body: detailJSON},
	}}
	result, err := New(client, " 試験用エージェント ", 64*1024).Fetch(context.Background(), domain.Source{
		ID: 42, MaxItems: 1, EndpointURL: "https://example.invalid/無視する", Settings: json.RawMessage(`不正な設定も使用しない`),
	})
	if err != nil || len(result.Warnings) != 0 || len(result.Articles) != 1 {
		test.Fatalf("Fisco 記事を取得できませんでした: result=%#v err=%v", result, err)
	}
	if len(client.requests) != 2 {
		test.Fatalf("最大件数を超えた詳細要求があります: %d", len(client.requests))
	}
	listing := client.requests[0]
	if listing.URL.Scheme != "https" || listing.URL.Host != "web.fisco.jp" || listing.URL.Path != "/FiscoWeb/news" ||
		listing.URL.Query().Get("isRemarkable") != "false" || listing.URL.Query().Get("limit") != "1" || len(listing.URL.Query()) != 2 {
		test.Errorf("一覧要求が仕様と異なります: %s", listing.URL)
	}
	if client.requests[1].URL.String() != EndpointURL+"/0009340020260908019" {
		test.Errorf("詳細要求が仕様と異なります: %s", client.requests[1].URL)
	}
	for _, request := range client.requests {
		if request.Method != http.MethodGet || request.Header.Get("User-Agent") != "試験用エージェント" ||
			request.Header.Get("Accept") != "application/json,text/plain,*/*" {
			test.Errorf("HTTP 要求のヘッダーまたはメソッドが不正です: %#v", request)
		}
	}

	// ----------------------------------------

	article := result.Articles[0]
	if article.SourceID != 42 || article.Provider != "fisco" || article.ExternalID != "0009340020260908019" ||
		article.CanonicalURL != articlePageURL+article.ExternalID || article.Headline != "一覧の見出し" ||
		article.BodyText != "銘柄<6905>が上昇。\n続きの全文です。" || article.UpdatedAt != nil {
		test.Errorf("記事の対応付けが仕様と異なります: %#v", article)
	}
	if article.PublishedAt == nil || article.PublishedAt.Format(time.RFC3339) != "2026-09-08T17:32:20+09:00" ||
		article.PublishedAt.UTC().Format(time.RFC3339) != "2026-09-08T08:32:20Z" {
		test.Errorf("日時を日本時間で解釈できていません: %v", article.PublishedAt)
	}
	if len(article.RevisionID) != 64 || string(article.RawPayload) != detailJSON {
		test.Errorf("リビジョンまたは詳細原文が保存されませんでした: %#v", article)
	}
}

// ----------------------------------------

// TestFetchRevisionDetectsContentChanges は、本文更新を取りこぼさず重複を識別することを検証します。
//
// 機能:
//   - 同じ一覧でも詳細 API を毎回取得する
//   - 同じ保存内容は同一リビジョン、見出し・日時・本文の変更は別リビジョンになる
//   - 関連記事など保存内容以外の変化はリビジョンに影響しない
//
// 引数:
//   - test (*testing.T): テスト状態
//
// 返り値:
//   - なし
func TestFetchRevisionDetectsContentChanges(test *testing.T) {
	cases := []struct {
		title, dateTime, body, metadata string
	}{
		{"見出し", "20260908173220", "最初の本文", "1"},
		{"見出し", "20260908173220", "最初の本文", "2"},
		{"見出し", "20260908173220", "更新された本文", "2"},
		{"見出し更新", "20260908173220", "最初の本文", "1"},
		{"見出し", "20260908173221", "最初の本文", "1"},
	}
	var revisions []string
	for _, testCase := range cases {
		client := &scriptedClient{responses: []scriptedResponse{
			{body: `{"data":[{"newsId":"記事IDの代わりに英数字を使う"}]}`},
			{body: `{"news":{"newsReport":"` + testCase.body + `"},"関連情報":` + testCase.metadata + `}`},
		}}
		listing, err := json.Marshal(map[string]any{"data": []articleSummary{{ID: "123", Title: testCase.title, DateTime: testCase.dateTime}}})
		if err != nil {
			test.Fatal(err)
		}
		client.responses[0].body = string(listing)
		result, err := New(client, "", 0).Fetch(context.Background(), domain.Source{ID: 1, MaxItems: 1})
		if err != nil || len(result.Articles) != 1 || len(client.requests) != 2 {
			test.Fatalf("詳細を毎回取得できませんでした: result=%#v err=%v", result, err)
		}
		revisions = append(revisions, result.Articles[0].RevisionID)
	}
	if revisions[0] != revisions[1] {
		test.Error("保存内容が同じでもリビジョンが変化しました")
	}
	for _, revision := range revisions[2:] {
		if revision == revisions[0] {
			test.Error("保存内容の変更でリビジョンが変化しませんでした")
		}
	}
}

// ----------------------------------------

// TestFetchContinuesAfterInvalidArticle は、個別記事の失敗後も後続記事を取得することを検証します。
//
// 機能:
//   - 必須一覧項目、危険な ID、日時、詳細エラーを個別警告にする
//   - サイズ超過、欠落した本文、詳細 ID 不一致を保存しない
//
// 引数:
//   - test (*testing.T): テスト状態
//
// 返り値:
//   - なし
func TestFetchContinuesAfterInvalidArticle(test *testing.T) {
	valid := articleSummary{ID: "123", Title: "見出し", DateTime: "20260908173220"}
	cases := []struct {
		name        string
		summary     articleSummary
		detail      *scriptedResponse
		limit       int64
		wantWarning string
	}{
		{name: "IDなし", summary: articleSummary{Title: valid.Title, DateTime: valid.DateTime}, wantWarning: "newsId"},
		{name: "パストラバーサル", summary: articleSummary{ID: "../123", Title: valid.Title, DateTime: valid.DateTime}, wantWarning: "newsId"},
		{name: "見出しなし", summary: articleSummary{ID: "123", DateTime: valid.DateTime}, wantWarning: "headline"},
		{name: "日時なし", summary: articleSummary{ID: "123", Title: valid.Title}, wantWarning: "newsDateTime"},
		{name: "日時不正", summary: articleSummary{ID: "123", Title: valid.Title, DateTime: "20260230173220"}, wantWarning: "newsDateTime"},
		{name: "HTTPエラー", summary: valid, detail: &scriptedResponse{status: 503, body: "取得失敗"}, wantWarning: "status=503"},
		{name: "JSON不正", summary: valid, detail: &scriptedResponse{body: "{"}, wantWarning: "JSON 解析"},
		{name: "詳細なし", summary: valid, detail: &scriptedResponse{body: `{"news":null}`}, wantWarning: "news"},
		{name: "本文なし", summary: valid, detail: &scriptedResponse{body: `{"news":{"newsReport":" "}}`}, wantWarning: "news.newsReport"},
		{name: "ID不一致", summary: valid, detail: &scriptedResponse{body: `{"news":{"newsId":"999","newsReport":"本文"}}`}, wantWarning: "一致"},
		{name: "レスポンス超過", summary: valid, detail: &scriptedResponse{body: strings.Repeat(" ", 2048)}, limit: 1024, wantWarning: "max_response_bytes=1024"},
		{name: "本文超過", summary: valid, detail: &scriptedResponse{body: `{"news":{"newsReport":"` + strings.Repeat("a", maxArticleBodyBytes+1) + `"}}`}, wantWarning: "body_text がサイズ上限"},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			listing, err := json.Marshal(map[string]any{"data": []articleSummary{testCase.summary, {ID: "456", Title: "後続の記事", DateTime: valid.DateTime}}})
			if err != nil {
				test.Fatal(err)
			}
			client := &scriptedClient{responses: []scriptedResponse{{body: string(listing)}}}
			if testCase.detail != nil {
				client.responses = append(client.responses, *testCase.detail)
			}
			client.responses = append(client.responses, scriptedResponse{body: `{"news":{"newsId":"456","newsReport":"後続の本文"}}`})
			result, err := New(client, "", testCase.limit).Fetch(context.Background(), domain.Source{MaxItems: 2})
			if err != nil || len(result.Warnings) != 1 || len(result.Articles) != 1 {
				test.Fatalf("後続の記事を継続取得できませんでした: result=%#v err=%v", result, err)
			}
			if result.Articles[0].ExternalID != "456" || !strings.Contains(result.Warnings[0], testCase.wantWarning) {
				test.Errorf("記事または警告が一致しません: %#v", result)
			}
		})
	}
}

// ----------------------------------------

// TestFetchRejectsInvalidListing は、一覧の取得失敗と空の正常一覧を区別することを検証します。
//
// 機能:
//   - HTTP エラー、JSON 不正、data 欠落、サイズ超過を全体エラーにする
//   - 有効な空配列と取得件数上限を受け入れる
//
// 引数:
//   - test (*testing.T): テスト状態
//
// 返り値:
//   - なし
func TestFetchRejectsInvalidListing(test *testing.T) {
	cases := []struct {
		name     string
		response scriptedResponse
		limit    int64
		want     string
	}{
		{name: "通信失敗", response: scriptedResponse{err: errors.New("通信できません")}, want: "通信できません"},
		{name: "HTTPエラー", response: scriptedResponse{status: 429, body: strings.Repeat("a", 8192)}, want: "status=429"},
		{name: "JSON不正", response: scriptedResponse{body: "{"}, want: "JSON 解析"},
		{name: "dataなし", response: scriptedResponse{body: `{}`}, want: "data"},
		{name: "dataがnull", response: scriptedResponse{body: `{"data":null}`}, want: "data"},
		{name: "data型不正", response: scriptedResponse{body: `{"data":{}}`}, want: "JSON 解析"},
		{name: "サイズ超過", response: scriptedResponse{body: `{"data":[]}`}, limit: 5, want: "max_response_bytes=5"},
		{name: "空一覧", response: scriptedResponse{body: `{"data":[]}`}},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			client := &scriptedClient{responses: []scriptedResponse{testCase.response}}
			result, err := New(client, "", testCase.limit).Fetch(context.Background(), domain.Source{MaxItems: 50})
			if testCase.want == "" {
				if err != nil || len(result.Articles) != 0 || len(result.Warnings) != 0 {
					test.Fatalf("空の正常一覧がエラーになりました: result=%#v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), testCase.want) {
				test.Errorf("一覧の失敗が適切に通知されません: %v", err)
			}
			if err != nil && len(err.Error()) > 5000 {
				test.Error("HTTP エラー本文のプレビューが制限されていません")
			}
			if client.requests[0].URL.Query().Get("limit") != "50" {
				test.Errorf("最大取得件数が limit に反映されません: %s", client.requests[0].URL)
			}
		})
	}
}

// ----------------------------------------

// TestFetchValidatesInput は、入力不備時に HTTP 通信しないことを検証します。
//
// 機能:
//   - nil コンテキスト、nil コネクタ、不正な件数を拒否する
//   - コンストラクタの未指定値を既定値で補完する
//
// 引数:
//   - test (*testing.T): テスト状態
//
// 返り値:
//   - なし
func TestFetchValidatesInput(test *testing.T) {
	client := &scriptedClient{}
	connector := New(client, "", 0)
	for _, count := range []int{-1, 0, 51} {
		if _, err := connector.Fetch(context.Background(), domain.Source{MaxItems: count}); err == nil {
			test.Errorf("不正な最大取得件数が拒否されません: %d", count)
		}
	}
	if _, err := connector.Fetch(nil, domain.Source{MaxItems: 1}); err == nil {
		test.Error("nil コンテキストが拒否されません")
	}
	for _, invalid := range []*Connector{nil, {}, {client: client, maxResponseBytes: -1}} {
		if _, err := invalid.Fetch(context.Background(), domain.Source{MaxItems: 1}); err == nil {
			test.Error("不正なコネクタが拒否されません")
		}
	}
	if len(client.requests) != 0 {
		test.Error("入力不備で HTTP 通信が実行されました")
	}
	defaults := New(nil, " ", -1)
	if defaults.client != http.DefaultClient || defaults.userAgent != defaultUserAgent || defaults.maxResponseBytes != defaultMaxResponseSize {
		test.Errorf("既定値が補完されません: %#v", defaults)
	}
}

// ----------------------------------------

// TestFetchHonorsCancellation は、キャンセルを警告に変換せず要求を停止することを検証します。
//
// 機能:
//   - 開始前と詳細取得中のキャンセル、期限切れを全体エラーにする
//   - 中断前に取得できた記事を保持し、後続要求を行わない
//
// 引数:
//   - test (*testing.T): テスト状態
//
// 返り値:
//   - なし
func TestFetchHonorsCancellation(test *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &scriptedClient{}
	if _, err := New(client, "", 0).Fetch(ctx, domain.Source{MaxItems: 1}); !errors.Is(err, context.Canceled) {
		test.Errorf("開始前キャンセルが伝播しません: %v", err)
	}
	if len(client.requests) != 0 {
		test.Error("開始前キャンセル後に HTTP 通信が実行されました")
	}

	// ----------------------------------------

	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		client = &scriptedClient{responses: []scriptedResponse{
			{body: `{"data":[{"newsId":"1","newsTitle":"見出し","newsDateTime":"20260908173220"}]}`},
			{err: cancellation},
		}}
		result, err := New(client, "", 0).Fetch(context.Background(), domain.Source{MaxItems: 1})
		if !errors.Is(err, cancellation) || len(result.Warnings) != 0 {
			test.Errorf("詳細通信の中断が全体エラーになりません: result=%#v err=%v", result, err)
		}
	}

	// ----------------------------------------

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	client = &scriptedClient{cancel: cancel, cancelAt: 2, responses: []scriptedResponse{
		{body: `{"data":[{"newsId":"1","newsTitle":"最初","newsDateTime":"20260908173220"},{"newsId":"2","newsTitle":"中断","newsDateTime":"20260908173220"},{"newsId":"3","newsTitle":"未取得","newsDateTime":"20260908173220"}]}`},
		{body: `{"news":{"newsReport":"最初の本文"}}`},
		{body: `{"news":{"newsReport":"中断する本文"}}`},
	}}
	result, err := New(client, "", 0).Fetch(ctx, domain.Source{MaxItems: 3})
	if !errors.Is(err, context.Canceled) || len(result.Articles) != 1 || len(result.Warnings) != 0 || len(client.requests) != 3 {
		test.Errorf("詳細取得中のキャンセルが適切に処理されません: result=%#v err=%v requests=%d", result, err, len(client.requests))
	}
}

// ----------------------------------------

// TestCompactRawPayload は、過大な詳細 JSON を有効なメタデータへ置き換えることを検証します。
//
// 機能:
//   - 元サイズとハッシュを残し、上限以内のレスポンスは独立したコピーにする
//
// 引数:
//   - test (*testing.T): テスト状態
//
// 返り値:
//   - なし
func TestCompactRawPayload(test *testing.T) {
	raw := []byte(`{"news":{}}`)
	copy := compactRawPayload(raw)
	raw[0] = ' '
	if string(copy) != `{"news":{}}` {
		test.Error("保存用原文が元バッファから独立していません")
	}
	large := []byte(strings.Repeat("a", maxRawPayloadBytes+1))
	var metadata struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"original_bytes"`
		SHA256        string `json:"sha256"`
	}
	if err := json.Unmarshal(compactRawPayload(large), &metadata); err != nil {
		test.Fatalf("サイズ超過メタデータが不正な JSON です: %v", err)
	}
	if !metadata.Truncated || metadata.OriginalBytes != len(large) || len(metadata.SHA256) != 64 {
		test.Errorf("サイズ超過メタデータが不足しています: %#v", metadata)
	}
}
