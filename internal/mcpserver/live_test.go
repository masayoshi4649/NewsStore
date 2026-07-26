package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/config"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

/*
TestLiveMCPReadsPostgreSQLArticles は、実PostgreSQLを使ったMCP記事参照を検証します。

機能:
  - NEWSSTORE_TEST_MCP_LIVE=1が指定された場合だけ実DBテストを実行する
  - 公式MCPクライアントから取得対象一覧と無条件の記事検索を呼び出す
  - 逆順日時を含む複合AND検索が入力順に依存しないことを確認する
  - 記事が存在する場合は先頭記事の本文断片も参照する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveMCPReadsPostgreSQLArticles(t *testing.T) {
	if os.Getenv("NEWSSTORE_TEST_MCP_LIVE") != "1" {
		t.Skip("MCP実DBテストはNEWSSTORE_TEST_MCP_LIVE=1の場合だけ実行します")
	}

	configDirectory := os.Getenv("NEWSSTORE_TEST_MCP_CONF")
	if configDirectory == "" {
		configDirectory = filepath.Join("..", "..", "conf")
	}
	cfg, err := config.LoadDir(configDirectory)
	if err != nil {
		t.Fatalf("MCPテスト設定を読み込めません: %v", err)
	}

	openCtx, cancelOpen := context.WithTimeout(context.Background(), 15*time.Second)
	store, err := postgres.Open(openCtx, cfg.Postgres)
	cancelOpen()
	if err != nil {
		t.Fatalf("PostgreSQLへ接続できません: %v", err)
	}
	defer store.Close()

	server := newMCPTestServerWithLimit(t, store, cfg.MCP.DefaultArticleLimit)
	session := connectMCPTestClient(t, server.Handler())
	callCtx, cancelCall := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancelCall()

	sourcesResult, err := session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      "list_sources",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(list_sources) error = %v", err)
	}
	if sourcesResult.IsError {
		t.Fatalf("CallTool(list_sources) IsError = true: %s", resultText(sourcesResult))
	}
	var sourcesOutput ListSourcesOutput
	decodeStructuredResult(t, sourcesResult, &sourcesOutput)
	for _, source := range sourcesOutput.Sources {
		if source.ID < 1 || source.Name == "" {
			t.Errorf("取得対象 = %+v, 正のIDと設定名を期待", source)
		}
	}

	// ----------------------------------------

	searchResult, err := session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      "search_articles",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(search_articles) error = %v", err)
	}
	if searchResult.IsError {
		t.Fatalf("CallTool(search_articles) IsError = true: %s", resultText(searchResult))
	}

	var searchOutput SearchArticlesOutput
	decodeStructuredResult(t, searchResult, &searchOutput)
	if len(searchOutput.Articles) > cfg.MCP.DefaultArticleLimit {
		t.Errorf(
			"記事件数 = %d, conf既定上限%dを超えています",
			len(searchOutput.Articles),
			cfg.MCP.DefaultArticleLimit,
		)
	}
	if len(searchOutput.Articles) == 0 {
		t.Log("保存済み記事がないため、取得対象一覧・検索通信・実SQLだけを確認しました")
		return
	}

	// ----------------------------------------

	firstArticle := searchOutput.Articles[0]
	if firstArticle.Headline != "" && firstArticle.BodyExcerpt != "" {
		headlineRunes := []rune(firstArticle.Headline)
		if len(headlineRunes) > 10 {
			headlineRunes = headlineRunes[:10]
		}
		bodyRunes := []rune(firstArticle.BodyExcerpt)
		if len(bodyRunes) > 10 {
			bodyRunes = bodyRunes[:10]
		}
		effectiveAt, parseErr := time.Parse(time.RFC3339Nano, firstArticle.EffectiveAt)
		if parseErr != nil {
			t.Fatalf("effective_atを解析できません: %v", parseErr)
		}
		combinedResult, callErr := session.CallTool(callCtx, &mcp.CallToolParams{
			Name: "search_articles",
			Arguments: map[string]any{
				"query":     string(headlineRunes),
				"keywords":  []any{string(bodyRunes)},
				"source_id": float64(firstArticle.SourceID),
				"date_from": effectiveAt.Add(time.Second).Format(time.RFC3339Nano),
				"date_to":   effectiveAt.Add(-time.Second).Format(time.RFC3339Nano),
				"limit":     float64(1),
			},
		})
		if callErr != nil {
			t.Fatalf("複合条件 CallTool(search_articles) error = %v", callErr)
		}
		if combinedResult.IsError {
			t.Fatalf(
				"複合条件 CallTool(search_articles) IsError = true: %s",
				resultText(combinedResult),
			)
		}
		var combinedOutput SearchArticlesOutput
		decodeStructuredResult(t, combinedResult, &combinedOutput)
		if len(combinedOutput.Articles) != 1 ||
			combinedOutput.Articles[0].ID != firstArticle.ID {
			t.Errorf(
				"複合条件の記事 = %+v, 記事ID %dを期待",
				combinedOutput.Articles,
				firstArticle.ID,
			)
		}
	}

	// ----------------------------------------

	articleID := firstArticle.ID
	detailResult, err := session.CallTool(callCtx, &mcp.CallToolParams{
		Name: "get_article",
		Arguments: map[string]any{
			"id":         float64(articleID),
			"body_limit": float64(100),
		},
	})
	if err != nil {
		t.Fatalf("CallTool(get_article) error = %v", err)
	}
	if detailResult.IsError {
		t.Fatalf("CallTool(get_article) IsError = true: %s", resultText(detailResult))
	}

	var detailOutput GetArticleOutput
	decodeStructuredResult(t, detailResult, &detailOutput)
	if detailOutput.Article.ID != articleID {
		t.Errorf(
			"get_article ID = %d, 期待値は %d",
			detailOutput.Article.ID,
			articleID,
		)
	}
	if len([]rune(detailOutput.Article.BodyText)) > 100 {
		t.Errorf(
			"本文文字数 = %d, 上限100を超えています",
			len([]rune(detailOutput.Article.BodyText)),
		)
	}
}
