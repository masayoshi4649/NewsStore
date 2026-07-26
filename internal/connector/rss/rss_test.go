package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/mmcdole/gofeed"
)

// requestSnapshot は、テストサーバーが受信したHTTPリクエストの検証対象を表します。
type requestSnapshot struct {
	Method    string
	Path      string
	Accept    string
	UserAgent string
}

// requestRecorder は、テストサーバーが受信したHTTPリクエストを並行安全に記録します。
type requestRecorder struct {
	mutex     sync.Mutex
	snapshots []requestSnapshot
}

// record は、HTTPリクエストの検証対象を記録します。
//
// 機能:
//   - メソッド、パス、Accept、User-Agentを並行安全に保存する
//
// 引数:
//   - request (*http.Request): テストサーバーが受信したHTTPリクエスト
//
// 返り値:
//   - なし
func (recorder *requestRecorder) record(request *http.Request) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	recorder.snapshots = append(recorder.snapshots, requestSnapshot{
		Method:    request.Method,
		Path:      request.URL.Path,
		Accept:    request.Header.Get("Accept"),
		UserAgent: request.Header.Get("User-Agent"),
	})
}

// ----------------------------------------

// values は、記録済みHTTPリクエストのスナップショットを返します。
//
// 機能:
//   - 内部スライスを複製して呼び出し元による変更から保護する
//
// 引数:
//   - なし
//
// 返り値:
//   - []requestSnapshot: 記録順のHTTPリクエスト一覧
func (recorder *requestRecorder) values() []requestSnapshot {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	result := make([]requestSnapshot, len(recorder.snapshots))
	copy(result, recorder.snapshots)
	return result
}

// ----------------------------------------

// testSource は、RSSコネクタテスト用の取得元を生成します。
//
// 機能:
//   - EndpointURL、MaxItems、Settingsを指定した共通Sourceを構築する
//   - SourceIDとKindへテスト共通値を設定する
//
// 引数:
//   - endpointURL (string): テスト用フィードURL
//   - maxItems (int): 処理する最大フィード項目数
//   - rawSettings (string): RSS固有設定JSON
//
// 返り値:
//   - domain.Source: RSSコネクタへ渡すテスト用取得元
func testSource(endpointURL string, maxItems int, rawSettings string) domain.Source {
	return domain.Source{
		ID:          42,
		Name:        "RSSテスト",
		Kind:        domain.SourceKindRSS,
		EndpointURL: endpointURL,
		MaxItems:    maxItems,
		Settings:    json.RawMessage(rawSettings),
	}
}

// ----------------------------------------

// TestFetchRSS2UsesContentAndHeaders は、RSS 2.0の取得とContent優先変換を検証します。
//
// 機能:
//   - content:encodedがdescriptionより優先されることを検証する
//   - HTTPメソッド、Accept、User-Agent、共通記事マッピングを検証する
//   - フィード項目が有効なJSONとしてRawPayloadへ保存されることを検証する
//
// 引数:
//   - test (*testing.T): RSS 2.0取得を検証するテスト
//
// 返り値:
//   - なし
func TestFetchRSS2UsesContentAndHeaders(test *testing.T) {
	recorder := &requestRecorder{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		recorder.record(request)
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprintf(writer, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel>
    <title>テストフィード</title>
    <link>%s</link>
    <description>テスト</description>
    <item>
      <title><![CDATA[ RSS <strong>見出し</strong> ]]></title>
      <guid>rss-guid-1</guid>
      <link>%s/article/1</link>
      <pubDate>Sat, 25 Jul 2026 12:34:56 +0900</pubDate>
      <content:encoded><![CDATA[
        <p>第1段落</p>
        <p>第2 <strong>段落</strong></p>
      ]]></content:encoded>
      <description><![CDATA[<p>説明文</p>]]></description>
    </item>
  </channel>
</rss>`, server.URL, server.URL)
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "NewsStore-Test/2.0", 64*1024).
		Fetch(context.Background(), testSource(server.URL+"/feed", 5, ""))
	if err != nil {
		test.Fatalf("RSS 2.0のFetchが失敗しました: %v", err)
	}

	requests := recorder.values()
	if len(requests) != 1 {
		test.Fatalf("フィードリクエスト数が一致しません: got=%d want=1", len(requests))
	}
	if requests[0].Method != http.MethodGet {
		test.Errorf("HTTPメソッドが一致しません: got=%q want=%q", requests[0].Method, http.MethodGet)
	}
	if requests[0].Path != "/feed" {
		test.Errorf("フィードパスが一致しません: got=%q want=%q", requests[0].Path, "/feed")
	}
	if requests[0].Accept != feedAcceptHeader {
		test.Errorf("Acceptヘッダーが一致しません: got=%q want=%q", requests[0].Accept, feedAcceptHeader)
	}
	if requests[0].UserAgent != "NewsStore-Test/2.0" {
		test.Errorf("User-Agentが一致しません: got=%q", requests[0].UserAgent)
	}

	if len(result.Warnings) != 0 {
		test.Errorf("想定外の警告が発生しました: %#v", result.Warnings)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("記事数が一致しません: got=%d want=1", len(result.Articles))
	}

	article := result.Articles[0]
	if article.SourceID != 42 {
		test.Errorf("SourceIDが一致しません: got=%d want=42", article.SourceID)
	}
	if article.Provider != rssProvider {
		test.Errorf("Providerが一致しません: got=%q want=%q", article.Provider, rssProvider)
	}
	if article.ExternalID != "rss-guid-1" {
		test.Errorf("GUIDがExternalIDへ設定されていません: got=%q", article.ExternalID)
	}
	if article.CanonicalURL != server.URL+"/article/1" {
		test.Errorf("CanonicalURLが一致しません: got=%q", article.CanonicalURL)
	}
	if article.Headline != "RSS 見出し" {
		test.Errorf("Headlineがプレーンテキスト化されていません: got=%q", article.Headline)
	}
	if article.BodyText != "第1段落\n第2 段落" {
		test.Errorf("Content本文が優先されていません: got=%q", article.BodyText)
	}
	if len(article.RevisionID) != 64 {
		test.Errorf("RevisionIDがSHA-256形式ではありません: got=%q", article.RevisionID)
	}

	expectedPublishedAt, parseErr := time.Parse(
		time.RFC1123Z,
		"Sat, 25 Jul 2026 12:34:56 +0900",
	)
	if parseErr != nil {
		test.Fatalf("テスト日時の解析に失敗しました: %v", parseErr)
	}
	if article.PublishedAt == nil || !article.PublishedAt.Equal(expectedPublishedAt) {
		test.Errorf("PublishedAtが一致しません: got=%v want=%v", article.PublishedAt, expectedPublishedAt)
	}
	if !json.Valid(article.RawPayload) {
		test.Errorf("RawPayloadが有効なJSONではありません: %q", string(article.RawPayload))
	}
	if !strings.Contains(string(article.RawPayload), `"guid":"rss-guid-1"`) {
		test.Errorf("RawPayloadにフィード項目が含まれていません: %q", string(article.RawPayload))
	}
}

// ----------------------------------------

// TestFetchAtomMapsDatesAndContent は、Atomエントリーの取得と日時変換を検証します。
//
// 機能:
//   - Atom id、link、published、updated、contentを共通記事へ割り当てる
//   - Atom contentをHTMLからプレーンテキストへ変換する
//
// 引数:
//   - test (*testing.T): Atom取得を検証するテスト
//
// 返り値:
//   - なし
func TestFetchAtomMapsDatesAndContent(test *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/atom+xml")
		_, _ = fmt.Fprintf(writer, `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atomテスト</title>
  <id>urn:feed:test</id>
  <updated>2026-07-25T10:00:00Z</updated>
  <entry>
    <title>Atom見出し</title>
    <id>urn:article:atom-1</id>
    <link rel="alternate" href="%s/atom/1"/>
    <published>2026-07-25T09:00:00+09:00</published>
    <updated>2026-07-25T10:30:00+09:00</updated>
    <content type="html">&lt;p&gt;Atom第1段落&lt;/p&gt;&lt;p&gt;Atom第2段落&lt;/p&gt;</content>
    <summary type="html">&lt;p&gt;Atom説明&lt;/p&gt;</summary>
  </entry>
</feed>`, server.URL)
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "atom-test", 64*1024).
		Fetch(context.Background(), testSource(server.URL, 1, `{}`))
	if err != nil {
		test.Fatalf("AtomのFetchが失敗しました: %v", err)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("Atom記事数が一致しません: got=%d want=1", len(result.Articles))
	}

	article := result.Articles[0]
	if article.ExternalID != "urn:article:atom-1" {
		test.Errorf("Atom idがExternalIDへ設定されていません: got=%q", article.ExternalID)
	}
	if article.BodyText != "Atom第1段落\nAtom第2段落" {
		test.Errorf("Atom contentが本文へ設定されていません: got=%q", article.BodyText)
	}

	expectedPublishedAt, _ := time.Parse(time.RFC3339, "2026-07-25T09:00:00+09:00")
	if article.PublishedAt == nil || !article.PublishedAt.Equal(expectedPublishedAt) {
		test.Errorf("Atom PublishedAtが一致しません: got=%v want=%v", article.PublishedAt, expectedPublishedAt)
	}
	expectedUpdatedAt, _ := time.Parse(time.RFC3339, "2026-07-25T10:30:00+09:00")
	if article.UpdatedAt == nil || !article.UpdatedAt.Equal(expectedUpdatedAt) {
		test.Errorf("Atom UpdatedAtが一致しません: got=%v want=%v", article.UpdatedAt, expectedUpdatedAt)
	}
}

// ----------------------------------------

// TestFetchAtomTextContentKeepsLiteralText は、Atomのテキスト本文をリテラルとして保持することを検証します。
//
// 機能:
//   - type=textのcontentをHTMLとして解釈しない
//   - 不等号とタグ形式の文字列を除去せず本文へ保存する
//
// 引数:
//   - test (*testing.T): Atomテキスト本文を検証するテスト
//
// 返り値:
//   - なし
func TestFetchAtomTextContentKeepsLiteralText(test *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/atom+xml")
		_, _ = fmt.Fprint(writer, `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atomテキスト</title>
  <id>urn:feed:text</id>
  <updated>2026-07-25T10:00:00Z</updated>
  <entry>
    <title>テキスト本文</title>
    <id>urn:article:text-1</id>
    <updated>2026-07-25T10:30:00Z</updated>
    <content type="text">2 &lt; 3 と &lt;p&gt;リテラル段落&lt;/p&gt;</content>
  </entry>
</feed>`)
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "atom-text-test", 64*1024).
		Fetch(context.Background(), testSource(server.URL, 1, `{}`))
	if err != nil {
		test.Fatalf("Atomテキスト本文のFetchが失敗しました: %v", err)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("Atomテキスト記事数が一致しません: got=%d want=1", len(result.Articles))
	}
	if result.Articles[0].BodyText != "2 < 3 と <p>リテラル段落</p>" {
		test.Errorf("Atomテキスト本文がリテラルとして保持されていません: got=%q", result.Articles[0].BodyText)
	}
}

// ----------------------------------------

// TestFetchFullArticlePrefersArticleElement は、リンク先HTMLの本文優先抽出を検証します。
//
// 機能:
//   - fetch_full_article有効時に記事リンクへGETする
//   - mainやbodyよりarticle要素内のp段落を優先する
//   - 記事ページ取得にもAcceptとUser-Agentを設定する
//
// 引数:
//   - test (*testing.T): リンク先本文取得を検証するテスト
//
// 返り値:
//   - なし
func TestFetchFullArticlePrefersArticleElement(test *testing.T) {
	recorder := &requestRecorder{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		recorder.record(request)
		switch request.URL.Path {
		case "/feed":
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Full</title><link>%s</link><description>Full</description>
<item><title>リンク先記事</title><guid>full-1</guid><link>%s/article</link>
<description><![CDATA[<p>フィード本文</p>]]></description></item>
</channel></rss>`, server.URL, server.URL)
		case "/article":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(writer, `<!doctype html><html><body>
<main><p>main本文</p><article><p> article 第1段落 </p>
<p>article <strong>第2段落</strong><script>除外</script></p></article></main>
<p>body本文</p></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "full-test-agent", 64*1024).
		Fetch(
			context.Background(),
			testSource(server.URL+"/feed", 1, `{"fetch_full_article":true}`),
		)
	if err != nil {
		test.Fatalf("リンク先本文のFetchが失敗しました: %v", err)
	}
	if len(result.Warnings) != 0 {
		test.Errorf("リンク先本文取得で警告が発生しました: %#v", result.Warnings)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("記事数が一致しません: got=%d want=1", len(result.Articles))
	}
	if result.Articles[0].BodyText != "article 第1段落\narticle 第2段落" {
		test.Errorf("article本文が優先されていません: got=%q", result.Articles[0].BodyText)
	}

	requests := recorder.values()
	if len(requests) != 2 {
		test.Fatalf("HTTPリクエスト数が一致しません: got=%d want=2", len(requests))
	}
	if requests[1].Path != "/article" {
		test.Errorf("記事リンクへリクエストしていません: got=%q", requests[1].Path)
	}
	if requests[1].Accept != articleAcceptHeader {
		test.Errorf("記事Acceptヘッダーが一致しません: got=%q want=%q", requests[1].Accept, articleAcceptHeader)
	}
	if requests[1].UserAgent != "full-test-agent" {
		test.Errorf("記事User-Agentが一致しません: got=%q", requests[1].UserAgent)
	}
}

// ----------------------------------------

// TestFetchFullArticleResolvesRelativeLink は、相対記事リンクの安全な解決を検証します。
//
// 機能:
//   - フィードURLを基準に相対記事リンクを絶対URLへ解決する
//   - 解決済みURLをCanonicalURLとリンク先本文取得に使用する
//
// 引数:
//   - test (*testing.T): 相対記事リンクの解決を検証するテスト
//
// 返り値:
//   - なし
func TestFetchFullArticleResolvesRelativeLink(test *testing.T) {
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		recorder.record(request)
		switch request.URL.Path {
		case "/feeds/news.xml":
			_, _ = fmt.Fprint(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Relative</title><link>/feeds/</link><description>Relative</description>
<item><title>相対リンク記事</title><guid>relative-1</guid><link>../articles/relative</link>
<description>フィード本文</description></item></channel></rss>`)
		case "/articles/relative":
			_, _ = fmt.Fprint(writer, `<html><body><article><p>相対リンク本文</p></article></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "relative-test", 64*1024).
		Fetch(
			context.Background(),
			testSource(server.URL+"/feeds/news.xml", 1, `{"fetch_full_article":true}`),
		)
	if err != nil {
		test.Fatalf("相対リンク記事のFetchが失敗しました: %v", err)
	}
	if len(result.Warnings) != 0 {
		test.Errorf("相対リンク解決で警告が発生しました: %#v", result.Warnings)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("相対リンク記事数が一致しません: got=%d want=1", len(result.Articles))
	}
	if result.Articles[0].CanonicalURL != server.URL+"/articles/relative" {
		test.Errorf("相対CanonicalURLが解決されていません: got=%q", result.Articles[0].CanonicalURL)
	}
	if result.Articles[0].BodyText != "相対リンク本文" {
		test.Errorf("相対リンク先本文が取得されていません: got=%q", result.Articles[0].BodyText)
	}
	if len(recorder.values()) != 2 {
		test.Errorf("相対リンク先へのリクエスト数が一致しません: %#v", recorder.values())
	}
}

// ----------------------------------------

// TestFetchFullArticleUsesRedirectedFeedURLAsBase は、リダイレクト後のフィードURLを基準に使用することを検証します。
//
// 機能:
//   - フィード取得時のHTTPリダイレクトを追跡する
//   - 最終レスポンスURLを基準に相対記事リンクを解決する
//   - 解決済みURLをCanonicalURLとリンク先本文取得に使用する
//
// 引数:
//   - test (*testing.T): リダイレクト後URLによる相対リンク解決を検証するテスト
//
// 返り値:
//   - なし
func TestFetchFullArticleUsesRedirectedFeedURLAsBase(test *testing.T) {
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		recorder.record(request)
		switch request.URL.Path {
		case "/feed-start":
			http.Redirect(writer, request, "/feeds/redirected/news.xml", http.StatusFound)
		case "/feeds/redirected/news.xml":
			_, _ = fmt.Fprint(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Redirect</title><link>/feeds/redirected/</link><description>Redirect</description>
<item><title>リダイレクト基準記事</title><guid>redirect-relative-1</guid><link>../articles/redirected</link>
<description>フィード本文</description></item></channel></rss>`)
		case "/feeds/articles/redirected":
			_, _ = fmt.Fprint(writer, `<html><body><article><p>リダイレクト後基準の本文</p></article></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "redirect-relative-test", 64*1024).
		Fetch(
			context.Background(),
			testSource(server.URL+"/feed-start", 1, `{"fetch_full_article":true}`),
		)
	if err != nil {
		test.Fatalf("リダイレクト後相対リンク記事のFetchが失敗しました: %v", err)
	}
	if len(result.Warnings) != 0 {
		test.Errorf("リダイレクト後の相対リンク解決で警告が発生しました: %#v", result.Warnings)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("リダイレクト後相対リンク記事数が一致しません: got=%d want=1", len(result.Articles))
	}

	expectedURL := server.URL + "/feeds/articles/redirected"
	if result.Articles[0].CanonicalURL != expectedURL {
		test.Errorf(
			"リダイレクト後URLを基準にCanonicalURLが解決されていません: got=%q want=%q",
			result.Articles[0].CanonicalURL,
			expectedURL,
		)
	}
	if result.Articles[0].BodyText != "リダイレクト後基準の本文" {
		test.Errorf("リダイレクト後基準の記事本文が取得されていません: got=%q", result.Articles[0].BodyText)
	}
	if len(recorder.values()) != 3 {
		test.Errorf("リダイレクトを含むHTTPリクエスト数が一致しません: %#v", recorder.values())
	}
}

// ----------------------------------------

// TestFetchFullArticleFallsBackToFeedBody は、リンク先取得失敗時の本文フォールバックを検証します。
//
// 機能:
//   - 記事ページの非2xxを個別警告へ変換する
//   - Contentが空の場合にDescriptionを本文として採用する
//   - 個別取得失敗後も記事を結果へ含める
//
// 引数:
//   - test (*testing.T): リンク先本文フォールバックを検証するテスト
//
// 返り値:
//   - なし
func TestFetchFullArticleFallsBackToFeedBody(test *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/feed":
			_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Fallback</title><link>%s</link><description>Fallback</description>
<item><title>フォールバック記事</title><guid>fallback-1</guid><link>%s/unavailable</link>
<description><![CDATA[<div><p>説明の第1段落</p><p>説明の第2段落</p></div>]]></description></item>
</channel></rss>`, server.URL, server.URL)
		case "/unavailable":
			http.Error(writer, "temporarily unavailable", http.StatusBadGateway)
		default:
			http.NotFound(writer, request)
		}
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "fallback-test", 64*1024).
		Fetch(
			context.Background(),
			testSource(server.URL+"/feed", 1, `{"fetch_full_article":true}`),
		)
	if err != nil {
		test.Fatalf("フォールバック可能な個別エラーが全体エラーになりました: %v", err)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("フォールバック記事数が一致しません: got=%d want=1", len(result.Articles))
	}
	if result.Articles[0].BodyText != "説明の第1段落\n説明の第2段落" {
		test.Errorf("Descriptionへフォールバックしていません: got=%q", result.Articles[0].BodyText)
	}
	if len(result.Warnings) != 1 {
		test.Fatalf("フォールバック警告数が一致しません: got=%d warnings=%#v", len(result.Warnings), result.Warnings)
	}
	if !strings.Contains(result.Warnings[0], "フォールバック") ||
		!strings.Contains(result.Warnings[0], "status=502") {
		test.Errorf("警告にフォールバック理由が含まれません: %q", result.Warnings[0])
	}
}

// ----------------------------------------

// TestFetchFullArticleWithoutParagraphsFallsBack は、リンク先に有効な段落がない場合を検証します。
//
// 機能:
//   - 2xx HTMLでもarticle、main、bodyにp段落がなければ抽出失敗として扱う
//   - 抽出失敗を警告へ変換してフィードContentへフォールバックする
//
// 引数:
//   - test (*testing.T): リンク先段落欠落時のフォールバックを検証するテスト
//
// 返り値:
//   - なし
func TestFetchFullArticleWithoutParagraphsFallsBack(test *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/feed":
			_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>NoParagraph</title><link>%s</link><description>NoParagraph</description>
<item><title>段落なし記事</title><guid>no-paragraph</guid><link>%s/no-paragraph</link>
<description><![CDATA[<p>フィード内本文</p>]]></description></item></channel></rss>`, server.URL, server.URL)
		case "/no-paragraph":
			_, _ = fmt.Fprint(writer, `<html><body><main><article><div>pではない本文</div></article></main></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "no-paragraph-test", 64*1024).
		Fetch(
			context.Background(),
			testSource(server.URL+"/feed", 1, `{"fetch_full_article":true}`),
		)
	if err != nil {
		test.Fatalf("段落欠落時のフォールバックが全体エラーになりました: %v", err)
	}
	if len(result.Articles) != 1 || result.Articles[0].BodyText != "フィード内本文" {
		test.Errorf("段落欠落後にフィード本文へ戻っていません: %#v", result.Articles)
	}
	if len(result.Warnings) != 1 ||
		!strings.Contains(result.Warnings[0], "有効なp段落") {
		test.Errorf("段落欠落の警告が一致しません: %#v", result.Warnings)
	}
}

// ----------------------------------------

// TestFetchFullArticleOversizeFallsBack は、リンク先レスポンスのサイズ上限を検証します。
//
// 機能:
//   - フィードより大きな記事ページがレスポンス上限を超えることを検出する
//   - サイズ超過を警告へ変換してフィードContentへフォールバックする
//
// 引数:
//   - test (*testing.T): 記事ページのサイズ制限を検証するテスト
//
// 返り値:
//   - なし
func TestFetchFullArticleOversizeFallsBack(test *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/feed":
			_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>S</title><link>%s</link><description>S</description>
<item><title>サイズ超過</title><guid>oversize-article</guid><link>%s/large</link>
<description><![CDATA[<p>フィード本文</p>]]></description></item></channel></rss>`, server.URL, server.URL)
		case "/large":
			_, _ = fmt.Fprint(writer, "<article><p>"+strings.Repeat("大", 512)+"</p></article>")
		default:
			http.NotFound(writer, request)
		}
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "oversize-article-test", 512).
		Fetch(
			context.Background(),
			testSource(server.URL+"/feed", 1, `{"fetch_full_article":true}`),
		)
	if err != nil {
		test.Fatalf("記事ページサイズ超過が全体エラーになりました: %v", err)
	}
	if len(result.Articles) != 1 || result.Articles[0].BodyText != "フィード本文" {
		test.Errorf("サイズ超過後にフィード本文へ戻っていません: %#v", result.Articles)
	}
	if len(result.Warnings) != 1 ||
		!strings.Contains(result.Warnings[0], "max_response_bytes=512") {
		test.Errorf("サイズ超過警告が一致しません: %#v", result.Warnings)
	}
}

// ----------------------------------------

// TestFetchKeepsExternalIDAndChangesRevision は、外部IDとリビジョンIDの安定性を検証します。
//
// 機能:
//   - GUIDとLinkがない項目で安定情報SHA-256が同じExternalIDになることを検証する
//   - 本文または更新日時が変わるとRevisionIDだけが変わることを検証する
//
// 引数:
//   - test (*testing.T): ID生成の安定性を検証するテスト
//
// 返り値:
//   - なし
func TestFetchKeepsExternalIDAndChangesRevision(test *testing.T) {
	var mutex sync.Mutex
	bodyText := "初版本文"
	updatedAt := "2026-07-25T10:00:00Z"

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		mutex.Lock()
		currentBody := bodyText
		currentUpdatedAt := updatedAt
		mutex.Unlock()

		_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>ID</title><link>https://example.test/feed</link><description>ID</description>
<item><title>安定見出し</title><pubDate>Sat, 25 Jul 2026 09:00:00 +0000</pubDate>
<lastBuildDate>%s</lastBuildDate><description><![CDATA[<p>%s</p>]]></description></item>
</channel></rss>`, currentUpdatedAt, currentBody)
	}))
	test.Cleanup(server.Close)

	connector := New(server.Client(), "id-test", 64*1024)
	source := testSource(server.URL, 1, "")
	first, firstErr := connector.Fetch(context.Background(), source)
	second, secondErr := connector.Fetch(context.Background(), source)
	if firstErr != nil || secondErr != nil {
		test.Fatalf("同一フィードの取得に失敗しました: first=%v second=%v", firstErr, secondErr)
	}
	if len(first.Articles) != 1 || len(second.Articles) != 1 {
		test.Fatalf("同一フィードの記事数が一致しません: first=%d second=%d", len(first.Articles), len(second.Articles))
	}
	if first.Articles[0].ExternalID != second.Articles[0].ExternalID {
		test.Errorf(
			"同一項目のExternalIDが変化しました: first=%q second=%q",
			first.Articles[0].ExternalID,
			second.Articles[0].ExternalID,
		)
	}
	if first.Articles[0].RevisionID != second.Articles[0].RevisionID {
		test.Errorf(
			"同一内容のRevisionIDが変化しました: first=%q second=%q",
			first.Articles[0].RevisionID,
			second.Articles[0].RevisionID,
		)
	}

	mutex.Lock()
	bodyText = "改訂本文"
	mutex.Unlock()

	changed, changedErr := connector.Fetch(context.Background(), source)
	if changedErr != nil {
		test.Fatalf("改訂フィードの取得に失敗しました: %v", changedErr)
	}
	if first.Articles[0].ExternalID != changed.Articles[0].ExternalID {
		test.Errorf("本文変更でExternalIDが変化しました")
	}
	if first.Articles[0].RevisionID == changed.Articles[0].RevisionID {
		test.Errorf("本文変更でRevisionIDが変化しませんでした")
	}
	if len(changed.Articles[0].ExternalID) != 64 {
		test.Errorf("フォールバックExternalIDがSHA-256形式ではありません: %q", changed.Articles[0].ExternalID)
	}

	firstUpdatedAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	secondUpdatedAt := firstUpdatedAt.Add(time.Minute)
	firstUpdatedRevision := buildRevisionID(
		&gofeed.Item{UpdatedParsed: &firstUpdatedAt},
		"更新日時テスト",
		"同一本文",
	)
	secondUpdatedRevision := buildRevisionID(
		&gofeed.Item{UpdatedParsed: &secondUpdatedAt},
		"更新日時テスト",
		"同一本文",
	)
	if firstUpdatedRevision == secondUpdatedRevision {
		test.Errorf("更新日時変更でRevisionIDが変化しませんでした")
	}
}

// ----------------------------------------

// TestFetchSkipsInvalidItemsWithinMaxItems は、項目単位の入力不備とMaxItemsを検証します。
//
// 機能:
//   - 見出しが空の項目と本文が空の項目を警告付きでスキップする
//   - 不正項目の後にある正常項目の処理を継続する
//   - MaxItemsより後ろの項目を処理しない
//
// 引数:
//   - test (*testing.T): 項目スキップと最大件数を検証するテスト
//
// 返り値:
//   - なし
func TestFetchSkipsInvalidItemsWithinMaxItems(test *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		_, _ = fmt.Fprint(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Skip</title><link>https://example.test</link><description>Skip</description>
<item><guid>missing-headline</guid><description>本文あり</description></item>
<item><title>本文なし</title><guid>missing-body</guid></item>
<item><title>正常記事</title><guid>valid-third</guid><description><![CDATA[<p>説明本文</p>]]></description></item>
<item><title>上限外記事</title><guid>outside-limit</guid><description>上限外本文</description></item>
</channel></rss>`)
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "skip-test", 64*1024).
		Fetch(context.Background(), testSource(server.URL, 3, ""))
	if err != nil {
		test.Fatalf("個別項目エラーが全体エラーになりました: %v", err)
	}
	if len(result.Warnings) != 2 {
		test.Errorf("個別項目警告数が一致しません: got=%d warnings=%#v", len(result.Warnings), result.Warnings)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("正常記事数が一致しません: got=%d want=1", len(result.Articles))
	}
	if result.Articles[0].ExternalID != "valid-third" {
		test.Errorf("MaxItems内の正常記事が取得されていません: %#v", result.Articles[0])
	}
}

// ----------------------------------------

// TestFetchRejectsNon2xxFeed は、フィードの非2xxレスポンス処理を検証します。
//
// 機能:
//   - フィードの非2xxステータスを取得全体のエラーにする
//   - エラーへHTTPステータスとサイズ内のレスポンス本文を含める
//
// 引数:
//   - test (*testing.T): 非2xxレスポンスを検証するテスト
//
// 返り値:
//   - なし
func TestFetchRejectsNon2xxFeed(test *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Error(writer, "feed unavailable", http.StatusServiceUnavailable)
	}))
	test.Cleanup(server.Close)

	_, err := New(server.Client(), "non-2xx-test", 64*1024).
		Fetch(context.Background(), testSource(server.URL, 1, ""))
	if err == nil {
		test.Fatal("非2xxフィードでエラーが返されませんでした")
	}
	if !strings.Contains(err.Error(), "status=503") ||
		!strings.Contains(err.Error(), "feed unavailable") {
		test.Errorf("非2xxエラーにステータスまたは本文が含まれません: %v", err)
	}
}

// ----------------------------------------

// TestFetchRejectsOversizedFeed は、フィードレスポンスのサイズ上限を検証します。
//
// 機能:
//   - 2xxレスポンスでも読み取り上限を超えたフィードを全体エラーにする
//   - エラーへ設定したmax_response_bytesを含める
//
// 引数:
//   - test (*testing.T): フィードサイズ上限を検証するテスト
//
// 返り値:
//   - なし
func TestFetchRejectsOversizedFeed(test *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		_, _ = fmt.Fprint(writer, strings.Repeat("x", 65))
	}))
	test.Cleanup(server.Close)

	_, err := New(server.Client(), "oversize-feed-test", 64).
		Fetch(context.Background(), testSource(server.URL, 1, ""))
	if err == nil {
		test.Fatal("サイズ上限超過フィードでエラーが返されませんでした")
	}
	if !strings.Contains(err.Error(), "max_response_bytes=64") {
		test.Errorf("サイズ上限エラーに設定値が含まれません: %v", err)
	}
}

// ----------------------------------------

// TestFetchValidatesEndpointAndArticleURLs は、フィードURLと記事URLの安全性検証を確認します。
//
// 機能:
//   - httpまたはhttps以外、hostなし、userinfo付きのフィードURLを拒否する
//   - 不正な記事URLを取得せず、フィード本文へ警告付きでフォールバックする
//
// 引数:
//   - test (*testing.T): URL検証を確認するテスト
//
// 返り値:
//   - なし
func TestFetchValidatesEndpointAndArticleURLs(test *testing.T) {
	invalidEndpoints := []string{
		"ftp://example.test/feed",
		"http:///feed",
		"http://:80/feed",
		"http://user:password@example.test/feed",
		"://broken",
	}
	for _, endpointURL := range invalidEndpoints {
		_, err := New(nil, "url-test", 64*1024).
			Fetch(context.Background(), testSource(endpointURL, 1, ""))
		if err == nil {
			test.Errorf("不正なフィードURLが拒否されません: %q", endpointURL)
		}
	}

	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		recorder.record(request)
		_, _ = fmt.Fprint(writer, `<?xml version="1.0"?>
<rss version="2.0"><channel><title>URL</title><link>https://example.test</link><description>URL</description>
<item><title>不正リンク記事</title><link>http://user:password@example.test/article</link>
<description><![CDATA[<p>安全なフィード本文</p>]]></description></item></channel></rss>`)
	}))
	test.Cleanup(server.Close)

	result, err := New(server.Client(), "url-test", 64*1024).
		Fetch(
			context.Background(),
			testSource(server.URL, 1, `{"fetch_full_article":true}`),
		)
	if err != nil {
		test.Fatalf("不正な個別リンクが全体エラーになりました: %v", err)
	}
	if len(recorder.values()) != 1 {
		test.Errorf("不正な記事リンクへHTTPリクエストが送られました: requests=%#v", recorder.values())
	}
	if len(result.Articles) != 1 || result.Articles[0].BodyText != "安全なフィード本文" {
		test.Errorf("不正リンクからフィード本文へ戻っていません: %#v", result.Articles)
	}
	if result.Articles[0].CanonicalURL != "" {
		test.Errorf("不正URLがCanonicalURLへ保存されています: %q", result.Articles[0].CanonicalURL)
	}
	if len(result.Warnings) < 2 {
		test.Errorf("URL拒否とフォールバックの警告が不足しています: %#v", result.Warnings)
	}
}

// ----------------------------------------

// TestFetchRejectsMaxItemsOutsideCommonRange は、共通最大取得件数の直接検証を確認します。
//
// 機能:
//   - 0件と共通上限50件を超える値をHTTP実行前に拒否する
//   - max_itemsを含む入力エラーを返す
//
// 引数:
//   - test (*testing.T): 最大取得件数の範囲を検証するテスト
//
// 返り値:
//   - なし
func TestFetchRejectsMaxItemsOutsideCommonRange(test *testing.T) {
	for _, maxItems := range []int{0, maxSourceItems + 1} {
		_, err := New(nil, "max-items-test", 64*1024).
			Fetch(
				context.Background(),
				testSource("https://example.test/feed", maxItems, ""),
			)
		if err == nil {
			test.Errorf("範囲外max_itemsが拒否されません: %d", maxItems)
			continue
		}
		if !strings.Contains(err.Error(), "max_items") {
			test.Errorf("max_itemsエラーの説明が含まれません: %v", err)
		}
	}
}

// ----------------------------------------

// TestExtractArticleParagraphsUsesMainThenBody は、articleがない場合の要素優先順位を検証します。
//
// 機能:
//   - article段落がない場合にmain段落を採用する
//   - articleとmainの段落がない場合にbody段落を採用する
//
// 引数:
//   - test (*testing.T): HTML本文抽出の要素優先順位を検証するテスト
//
// 返り値:
//   - なし
func TestExtractArticleParagraphsUsesMainThenBody(test *testing.T) {
	testCases := []struct {
		name     string
		source   string
		expected string
	}{
		{
			name:     "mainを採用",
			source:   `<html><body><main><p>main本文</p></main><p>body本文</p></body></html>`,
			expected: "main本文",
		},
		{
			name:     "bodyを採用",
			source:   `<html><body><div><p>body本文</p></div></body></html>`,
			expected: "body本文",
		},
	}

	for _, testCase := range testCases {
		test.Run(testCase.name, func(test *testing.T) {
			actual, err := extractArticleParagraphs([]byte(testCase.source))
			if err != nil {
				test.Fatalf("HTML本文抽出に失敗しました: %v", err)
			}
			if actual != testCase.expected {
				test.Errorf("抽出本文が一致しません: got=%q want=%q", actual, testCase.expected)
			}
		})
	}
}
