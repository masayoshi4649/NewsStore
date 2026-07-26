package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/articlesearch"
	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeArticleReader は、MCP サーバーテスト用の記事 Repository です。
type fakeArticleReader struct {
	mutex          sync.Mutex
	searchPage     postgres.MCPArticlePage
	searchError    error
	searchQueries  []postgres.MCPArticleSearchQuery
	sources        []postgres.MCPSource
	sourcesError   error
	sourceCalls    int
	articleChunk   postgres.MCPArticleChunk
	articleError   error
	articleIDs     []int64
	articleOffsets []int
	articleLimits  []int
}

/*
SearchArticlesForMCP は、設定済みの記事ページを返して検索条件を記録します。

機能:
  - MCP サーバーから渡された検索条件をテスト検証用に保存する
  - テストケースで設定した記事ページまたはエラーを返す

引数:
  - ctx context.Context: 呼び出し元の期限とキャンセルを伝えるコンテキスト
  - query postgres.MCPArticleSearchQuery: MCP から渡された検索条件

返り値:
  - postgres.MCPArticlePage: テストケースで設定した記事ページ
  - error: テストケースで設定したエラー
*/
func (f *fakeArticleReader) SearchArticlesForMCP(
	_ context.Context,
	query postgres.MCPArticleSearchQuery,
) (postgres.MCPArticlePage, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.searchQueries = append(f.searchQueries, query)
	return f.searchPage, f.searchError
}

// ----------------------------------------

/*
ListSourcesForMCP は、設定済みの取得対象一覧を返して呼び出し回数を記録します。

機能:
  - MCP サーバーからの取得対象一覧呼び出しをテスト検証用に記録する
  - テストケースで設定した取得対象一覧またはエラーを返す

引数:
  - ctx context.Context: 呼び出し元の期限とキャンセルを伝えるコンテキスト

返り値:
  - []postgres.MCPSource: テストケースで設定した取得対象一覧
  - error: テストケースで設定したエラー
*/
func (f *fakeArticleReader) ListSourcesForMCP(
	_ context.Context,
) ([]postgres.MCPSource, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.sourceCalls++
	return f.sources, f.sourcesError
}

// ----------------------------------------

/*
GetArticleChunkForMCP は、設定済みの記事本文断片を返して参照条件を記録します。

機能:
  - MCP サーバーから渡された記事IDと本文範囲をテスト検証用に保存する
  - テストケースで設定した記事本文断片またはエラーを返す

引数:
  - ctx context.Context: 呼び出し元の期限とキャンセルを伝えるコンテキスト
  - id int64: MCP から渡された記事ID
  - bodyOffset int: MCP から渡された本文開始位置
  - bodyLimit int: MCP から渡された本文上限

返り値:
  - postgres.MCPArticleChunk: テストケースで設定した記事本文断片
  - error: テストケースで設定したエラー
*/
func (f *fakeArticleReader) GetArticleChunkForMCP(
	_ context.Context,
	id int64,
	bodyOffset int,
	bodyLimit int,
) (postgres.MCPArticleChunk, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.articleIDs = append(f.articleIDs, id)
	f.articleOffsets = append(f.articleOffsets, bodyOffset)
	f.articleLimits = append(f.articleLimits, bodyLimit)
	return f.articleChunk, f.articleError
}

// ----------------------------------------

/*
TestServerListsOnlyReadToolsAndSearchesWithCursor は、MCP の公開ツールと検索カーソルを検証します。

機能:
  - 公式 MCP クライアントで Streamable HTTP へ接続する
  - 読み取り専用の3ツールだけが公開されることを確認する
  - 複合検索条件、構造化結果、日時とIDのカーソル往復を確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerListsOnlyReadToolsAndSearchesWithCursor(t *testing.T) {
	article := newMCPTestArticle()
	effectiveAt := article.FetchedAt
	reader := &fakeArticleReader{
		searchPage: postgres.MCPArticlePage{
			Items: []postgres.MCPStoredArticle{
				{
					StoredArticle: article,
					EffectiveAt:   effectiveAt,
				},
			},
			HasMore: true,
		},
	}
	server := newMCPTestServer(t, reader)
	session := connectMCPTestClient(t, server.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initializeResult := session.InitializeResult()
	if initializeResult == nil || initializeResult.Capabilities == nil {
		t.Fatal("InitializeResult の capabilities がありません")
	}
	capabilities := initializeResult.Capabilities
	if capabilities.Tools == nil || capabilities.Tools.ListChanged {
		t.Errorf("tools capability = %+v, 静的なtool一覧を期待", capabilities.Tools)
	}
	if capabilities.Logging != nil ||
		capabilities.Prompts != nil ||
		capabilities.Resources != nil ||
		capabilities.Completions != nil {
		t.Errorf("不要な capability が公開されています: %+v", capabilities)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("ListTools() length = %d, 期待値は 3", len(tools.Tools))
	}
	toolByName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		toolByName[tool.Name] = tool
	}
	for _, name := range []string{"list_sources", "search_articles", "get_article"} {
		tool := toolByName[name]
		if tool == nil {
			t.Fatalf("ListTools() に %q がありません", name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s の readOnlyHint が true ではありません", name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Errorf("%s の destructiveHint が false ではありません", name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s の openWorldHint が false ではありません", name)
		}
	}
	if !strings.Contains(
		toolByName["search_articles"].Description,
		"日時を両方指定した場合は入力順を問わず",
	) {
		t.Error("search_articlesの説明に日時範囲の入力順不問仕様がありません")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_articles",
		Arguments: map[string]any{
			"query":     "  経済  ",
			"keywords":  []any{" 政策 ", "経済"},
			"source_id": float64(7),
			"date_from": "2026-07-27T00:00:00+09:00",
			"date_to":   "2026-07-25T00:00:00+09:00",
			"limit":     float64(1),
		},
	})
	if err != nil {
		t.Fatalf("CallTool(search_articles) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool(search_articles) IsError = true: %s", resultText(result))
	}

	var output SearchArticlesOutput
	decodeStructuredResult(t, result, &output)
	if len(output.Articles) != 1 || output.Articles[0].ID != article.ID {
		t.Fatalf("検索結果 = %+v, 記事ID %d を期待", output.Articles, article.ID)
	}
	if output.Articles[0].BodyExcerpt != article.BodyText {
		t.Errorf(
			"body_excerpt = %q, 期待値は %q",
			output.Articles[0].BodyExcerpt,
			article.BodyText,
		)
	}
	if output.Articles[0].EffectiveAt != effectiveAt.Format(time.RFC3339Nano) {
		t.Errorf(
			"effective_at = %q, 期待値は %q",
			output.Articles[0].EffectiveAt,
			effectiveAt.Format(time.RFC3339Nano),
		)
	}
	if output.NextCursor == "" || !output.HasMore {
		t.Errorf("ページ情報 = %+v, 後続カーソルを期待", output)
	}

	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_articles",
		Arguments: map[string]any{
			"cursor": output.NextCursor,
		},
	})
	if err != nil {
		t.Fatalf("2回目 CallTool(search_articles) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("2回目 CallTool(search_articles) IsError = true: %s", resultText(result))
	}

	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if len(reader.searchQueries) != 2 {
		t.Fatalf("検索呼び出し回数 = %d, 期待値は 2", len(reader.searchQueries))
	}
	firstQuery := reader.searchQueries[0]
	if len(firstQuery.Keywords) != 2 ||
		firstQuery.Keywords[0] != "経済" ||
		firstQuery.Keywords[1] != "政策" ||
		firstQuery.SourceID != 7 ||
		firstQuery.Limit != 1 {
		t.Errorf("最初の検索条件 = %+v, 正規化済み条件を期待", firstQuery)
	}
	wantFrom := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	if firstQuery.PublishedFrom == nil ||
		!firstQuery.PublishedFrom.Equal(wantFrom) ||
		firstQuery.PublishedTo == nil ||
		!firstQuery.PublishedTo.Equal(wantTo) {
		t.Errorf(
			"日時条件 = from:%v to:%v, 逆順入力をUTCの時系列順へ正規化した条件を期待",
			firstQuery.PublishedFrom,
			firstQuery.PublishedTo,
		)
	}
	secondQuery := reader.searchQueries[1]
	if secondQuery.BeforeID != article.ID ||
		secondQuery.BeforeTime == nil ||
		!secondQuery.BeforeTime.Equal(effectiveAt) ||
		secondQuery.Limit != 20 {
		t.Errorf(
			"2回目の検索条件 = %+v, BeforeTime=%s BeforeID=%d Limit=20 を期待",
			secondQuery,
			effectiveAt,
			article.ID,
		)
	}
}

// ----------------------------------------

/*
TestServerListsSourceNamesAndIDs は、取得対象設定一覧の公開項目を検証します。

機能:
  - list_sourcesが取得対象ID、設定名、種別、有効状態を返すことを確認する
  - Repositoryの一覧操作が1回だけ呼ばれることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerListsSourceNamesAndIDs(t *testing.T) {
	reader := &fakeArticleReader{
		sources: []postgres.MCPSource{
			{
				ID:      7,
				Name:    "ロイター経済",
				Kind:    "reuters",
				Enabled: true,
			},
			{
				ID:      8,
				Name:    "停止中RSS",
				Kind:    "rss",
				Enabled: false,
			},
		},
	}
	server := newMCPTestServer(t, reader)
	session := connectMCPTestClient(t, server.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_sources",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(list_sources) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool(list_sources) IsError = true: %s", resultText(result))
	}

	var output ListSourcesOutput
	decodeStructuredResult(t, result, &output)
	if len(output.Sources) != 2 {
		t.Fatalf("sources length = %d, 期待値は2", len(output.Sources))
	}
	if output.Sources[0].ID != 7 ||
		output.Sources[0].Name != "ロイター経済" ||
		output.Sources[1].ID != 8 ||
		output.Sources[1].Enabled {
		t.Errorf("sources = %+v, ID・設定名・無効設定を含む一覧を期待", output.Sources)
	}

	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.sourceCalls != 1 {
		t.Errorf("取得対象一覧の呼び出し回数 = %d, 期待値は1", reader.sourceCalls)
	}
}

// ----------------------------------------

/*
TestServerUsesConfiguredDefaultArticleLimit は、無条件検索の既定件数を検証します。

機能:
  - 全条件省略時にサーバーへ注入したconf設定値をRepositoryへ渡す

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerUsesConfiguredDefaultArticleLimit(t *testing.T) {
	reader := &fakeArticleReader{}
	server := newMCPTestServerWithLimit(t, reader, 37)
	session := connectMCPTestClient(t, server.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_articles",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(search_articles) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool(search_articles) IsError = true: %s", resultText(result))
	}

	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if len(reader.searchQueries) != 1 {
		t.Fatalf("検索呼び出し回数 = %d, 期待値は1", len(reader.searchQueries))
	}
	query := reader.searchQueries[0]
	if query.Limit != 37 ||
		query.SourceID != 0 ||
		len(query.Keywords) != 0 ||
		query.PublishedFrom != nil ||
		query.PublishedTo != nil ||
		query.BeforeTime != nil ||
		query.BeforeID != 0 {
		t.Errorf("無条件検索 = %+v, Limit=37だけを期待", query)
	}
}

// ----------------------------------------

/*
TestServerGetsOnlyRequestedArticleChunk は、記事本文の分割参照を検証します。

機能:
  - 公式 MCP クライアントから記事IDと本文範囲を指定する
  - Repositoryへ同じ範囲が渡ることを確認する
  - 構造化結果に本文断片と次の本文位置が含まれることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerGetsOnlyRequestedArticleChunk(t *testing.T) {
	article := newMCPTestArticle()
	article.BodyText = "記事本文の一部分"
	reader := &fakeArticleReader{
		articleChunk: postgres.MCPArticleChunk{
			StoredArticle:  article,
			BodyOffset:     100,
			NextBodyOffset: 104,
			HasMore:        true,
		},
	}
	server := newMCPTestServer(t, reader)
	session := connectMCPTestClient(t, server.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_article",
		Arguments: map[string]any{
			"id":          float64(article.ID),
			"body_offset": float64(100),
			"body_limit":  float64(4),
		},
	})
	if err != nil {
		t.Fatalf("CallTool(get_article) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool(get_article) IsError = true: %s", resultText(result))
	}

	var output GetArticleOutput
	decodeStructuredResult(t, result, &output)
	if output.Article.ID != article.ID || output.Article.BodyText != article.BodyText {
		t.Errorf("記事詳細 = %+v, 記事IDと本文が一致しません", output.Article)
	}
	if !output.Article.HasMore || output.Article.NextBodyOffset != 104 {
		t.Errorf("本文ページ情報 = %+v, 次の位置104を期待", output.Article)
	}

	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if len(reader.articleIDs) != 1 ||
		reader.articleIDs[0] != article.ID ||
		reader.articleOffsets[0] != 100 ||
		reader.articleLimits[0] != 4 {
		t.Errorf(
			"Repository参照条件 = ids:%v offsets:%v limits:%v",
			reader.articleIDs,
			reader.articleOffsets,
			reader.articleLimits,
		)
	}
}

// ----------------------------------------

/*
TestServerRejectsInvalidToolInputWithoutRepositoryCall は、不正な入力を早期拒否することを検証します。

機能:
  - 上限超過の検索件数、不正な日時形式、不正な記事IDをツールエラーとして返す
  - 入力不正時に Repository を呼び出さないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerRejectsInvalidToolInputWithoutRepositoryCall(t *testing.T) {
	reader := &fakeArticleReader{}
	server := newMCPTestServer(t, reader)
	session := connectMCPTestClient(t, server.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testCases := []struct {
		name      string
		toolName  string
		arguments map[string]any
	}{
		{
			name:     "検索件数上限超過",
			toolName: "search_articles",
			arguments: map[string]any{
				"limit": float64(articlesearch.MaxLimit + 1),
			},
		},
		{
			name:     "検索語数上限超過",
			toolName: "search_articles",
			arguments: map[string]any{
				"keywords": []any{"1", "2", "3", "4", "5", "6"},
			},
		},
		{
			name:      "検索日時形式不正",
			toolName:  "search_articles",
			arguments: map[string]any{"date_from": "2026-07-26"},
		},
		{
			name:      "取得対象ID不正",
			toolName:  "search_articles",
			arguments: map[string]any{"source_id": float64(-1)},
		},
		{
			name:      "旧版カーソル",
			toolName:  "search_articles",
			arguments: map[string]any{"cursor": "djE6OTk"},
		},
		{
			name:      "記事ID不正",
			toolName:  "get_article",
			arguments: map[string]any{"id": float64(0)},
		},
		{
			name:     "本文位置上限超過",
			toolName: "get_article",
			arguments: map[string]any{
				"id":          float64(1),
				"body_offset": float64(maxArticleBodyOffset + 1),
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      testCase.toolName,
				Arguments: testCase.arguments,
			})
			if err != nil {
				t.Fatalf("CallTool(%s) error = %v", testCase.toolName, err)
			}
			if !result.IsError {
				t.Errorf("CallTool(%s) IsError = false, trueを期待", testCase.toolName)
			}
		})
	}

	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if len(reader.searchQueries) != 0 || len(reader.articleIDs) != 0 {
		t.Errorf(
			"不正入力でRepositoryが呼ばれました: searches=%d articles=%d",
			len(reader.searchQueries),
			len(reader.articleIDs),
		)
	}
}

// ----------------------------------------

/*
TestServerMasksRepositoryErrors は、内部エラーをMCPへ漏らさないことを検証します。

機能:
  - Repositoryの詳細なエラーをサーバーログだけへ記録する
  - MCPのツール結果には安全な概要だけを返す

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerMasksRepositoryErrors(t *testing.T) {
	reader := &fakeArticleReader{
		searchError: errors.New("秘密の接続情報 password=example"),
	}
	server := newMCPTestServer(t, reader)
	session := connectMCPTestClient(t, server.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search_articles",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(search_articles) error = %v", err)
	}
	if !result.IsError {
		t.Fatal("CallTool(search_articles) IsError = false, trueを期待")
	}
	text := resultText(result)
	if strings.Contains(text, "password") || !strings.Contains(text, "記事を検索できません") {
		t.Errorf("ツールエラー = %q, 内部情報を含まない安全な文言を期待", text)
	}
}

// ----------------------------------------

/*
TestServerProtectsHTTPEndpoint は、MCP HTTP 境界の制限を検証します。

機能:
  - POST以外、JSON以外、圧縮要求、過大本文を拒否する
  - 各拒否応答に想定したHTTP状態コードを返す

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerProtectsHTTPEndpoint(t *testing.T) {
	server := newMCPTestServer(t, &fakeArticleReader{})
	largeBody := strings.Repeat("x", int(maxRequestBodyBytes)+1)
	testCases := []struct {
		name        string
		method      string
		body        string
		contentType string
		encoding    string
		wantStatus  int
		wantAllow   string
	}{
		{
			name:       "GET",
			method:     http.MethodGet,
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodPost,
		},
		{
			name:        "JSON以外",
			method:      http.MethodPost,
			body:        "{}",
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "圧縮要求",
			method:      http.MethodPost,
			body:        "{}",
			contentType: "application/json",
			encoding:    "gzip",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "過大本文",
			method:      http.MethodPost,
			body:        largeBody,
			contentType: "application/json",
			wantStatus:  http.StatusRequestEntityTooLarge,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(
				testCase.method,
				"http://localhost/mcp",
				strings.NewReader(testCase.body),
			)
			if testCase.contentType != "" {
				request.Header.Set("Content-Type", testCase.contentType)
			}
			if testCase.encoding != "" {
				request.Header.Set("Content-Encoding", testCase.encoding)
			}
			recorder := httptest.NewRecorder()

			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != testCase.wantStatus {
				t.Errorf(
					"状態コード = %d, 期待値は %d: %s",
					recorder.Code,
					testCase.wantStatus,
					recorder.Body.String(),
				)
			}
			if recorder.Header().Get("Allow") != testCase.wantAllow {
				t.Errorf(
					"Allow = %q, 期待値は %q",
					recorder.Header().Get("Allow"),
					testCase.wantAllow,
				)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Error("Cache-Control = no-store が設定されていません")
			}
		})
	}
}

// ----------------------------------------

/*
TestServerDoesNotIssueSessionID は、ステートレスな初期化応答を検証します。

機能:
  - Originヘッダー付きのMCP initialize要求をHTTPで直接送信する
  - Originヘッダーの有無や値を検証せず、正常に処理する
  - 正常なJSON応答にセッションIDが付与されないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestServerDoesNotIssueSessionID(t *testing.T) {
	server := newMCPTestServer(t, &fakeArticleReader{})
	body := `{
		"jsonrpc":"2.0",
		"id":1,
		"method":"initialize",
		"params":{
			"protocolVersion":"2025-11-25",
			"capabilities":{},
			"clientInfo":{"name":"test-client","version":"1.0.0"}
		}
	}`
	request := httptest.NewRequest(
		http.MethodPost,
		"http://localhost/mcp",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Origin", "https://example.com")
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状態コード = %d, 期待値は200: %s", recorder.Code, recorder.Body.String())
	}
	if sessionID := recorder.Header().Get("Mcp-Session-Id"); sessionID != "" {
		t.Errorf("Mcp-Session-Id = %q, 空文字を期待", sessionID)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(
		contentType,
		"application/json",
	) {
		t.Errorf("Content-Type = %q, application/jsonを期待", contentType)
	}
	if !strings.Contains(recorder.Body.String(), `"protocolVersion":"2025-11-25"`) {
		t.Errorf(
			"initialize応答が2025-11-25を合意していません: %s",
			recorder.Body.String(),
		)
	}
}

// ----------------------------------------

/*
newMCPTestServer は、ログを破棄するテスト用MCPサーバーを生成します。

機能:
  - 指定した偽Repositoryと破棄ロガーでMCPサーバーを初期化する
  - 初期化失敗時にテストを直ちに失敗させる

引数:
  - t *testing.T: テスト状態を管理する値
  - reader ArticleReader: テストで利用する記事Repository

返り値:
  - *Server: 初期化済みのテスト用MCPサーバー
*/
func newMCPTestServer(t *testing.T, reader ArticleReader) *Server {
	t.Helper()
	return newMCPTestServerWithLimit(t, reader, 20)
}

// ----------------------------------------

/*
newMCPTestServerWithLimit は、検索既定件数を指定したテスト用MCPサーバーを生成します。

機能:
  - 指定した偽Repository、検索既定件数、破棄ロガーでMCPサーバーを初期化する
  - 初期化失敗時にテストを直ちに失敗させる

引数:
  - t *testing.T: テスト状態を管理する値
  - reader ArticleReader: テストで利用する記事Repository
  - defaultArticleLimit int: 検索条件を省略した場合の記事件数

返り値:
  - *Server: 初期化済みのテスト用MCPサーバー
*/
func newMCPTestServerWithLimit(
	t *testing.T,
	reader ArticleReader,
	defaultArticleLimit int,
) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := New(reader, defaultArticleLimit, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server
}

// ----------------------------------------

/*
connectMCPTestClient は、公式SDKのクライアントをテストHTTPサーバーへ接続します。

機能:
  - httptest.ServerでMCPハンドラーを公開する
  - SSEと再試行を無効にした公式MCPクライアントを接続する
  - テスト終了時にセッションとHTTPサーバーを閉じる

引数:
  - t *testing.T: テスト状態を管理する値
  - handler http.Handler: 公開するMCPハンドラー

返り値:
  - *mcp.ClientSession: 初期化済みの公式MCPクライアントセッション
*/
func connectMCPTestClient(
	t *testing.T,
	handler http.Handler,
) *mcp.ClientSession {
	t.Helper()
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "newsstore-test-client",
			Version: "1.0.0",
		},
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Connect(
		ctx,
		&mcp.StreamableClientTransport{
			Endpoint:             httpServer.URL,
			HTTPClient:           httpServer.Client(),
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Client.Connect() error = %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("ClientSession.Close() error = %v", err)
		}
	})
	return session
}

// ----------------------------------------

/*
decodeStructuredResult は、MCPの構造化結果を指定したGo値へ変換します。

機能:
  - StructuredContentをJSONとして再符号化する
  - 指定した出力型へ復号できない場合にテストを失敗させる

引数:
  - t *testing.T: テスト状態を管理する値
  - result *mcp.CallToolResult: MCPツールの呼び出し結果
  - output any: 復号先へのポインター

返り値:
  - なし
*/
func decodeStructuredResult(
	t *testing.T,
	result *mcp.CallToolResult,
	output any,
) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("StructuredContentのJSON化 error = %v", err)
	}
	if err := json.Unmarshal(data, output); err != nil {
		t.Fatalf("StructuredContentの復号 error = %v, JSON = %s", err, data)
	}
}

// ----------------------------------------

/*
resultText は、MCPツール結果のテキスト表現を返します。

機能:
  - 結果全体をJSONへ変換して検証しやすい文字列にする
  - JSON化に失敗した場合は空文字を返す

引数:
  - result *mcp.CallToolResult: MCPツールの呼び出し結果

返り値:
  - string: MCPツール結果のJSON文字列
*/
func resultText(result *mcp.CallToolResult) string {
	data, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(data)
}

// ----------------------------------------

/*
newMCPTestArticle は、MCPサーバーテスト用の記事を生成します。

機能:
  - 記事概要と詳細の全公開項目を持つ代表データを生成する

引数:
  - なし

返り値:
  - postgres.StoredArticle: MCPへ返せる代表記事
*/
func newMCPTestArticle() postgres.StoredArticle {
	publishedAt := time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC)
	updatedAt := publishedAt.Add(time.Hour)
	return postgres.StoredArticle{
		ID:         99,
		SourceName: "ロイター経済",
		Article: domain.Article{
			SourceID:     7,
			Provider:     "reuters",
			ExternalID:   "external-99",
			RevisionID:   "revision-99",
			CanonicalURL: "https://jp.reuters.com/example/99",
			PublishedAt:  &publishedAt,
			UpdatedAt:    &updatedAt,
			Headline:     "経済記事",
			BodyText:     "記事本文の抜粋",
		},
		FetchedAt: updatedAt.Add(time.Minute),
	}
}
