package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/config"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

/*
TestLiveArticleAPIReadsPostgreSQL は、実PostgreSQLを使った通常画面の記事APIを検証します。

機能:
  - NEWSSTORE_TEST_WEB_API_LIVE=1が指定された場合だけ実DBテストを実行する
  - 取得対象一覧、無条件の記事検索、逆順日時を含む全条件AND検索をHTTP経由で確認する
  - 記事が存在する場合は本文を指定文字数だけ分割取得する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveArticleAPIReadsPostgreSQL(t *testing.T) {
	if os.Getenv("NEWSSTORE_TEST_WEB_API_LIVE") != "1" {
		t.Skip("通常画面の記事API実DBテストはNEWSSTORE_TEST_WEB_API_LIVE=1の場合だけ実行します")
	}
	gin.SetMode(gin.TestMode)

	configDirectory := os.Getenv("NEWSSTORE_TEST_WEB_API_CONF")
	if configDirectory == "" {
		configDirectory = filepath.Join("..", "..", "conf")
	}
	cfg, err := config.LoadDir(configDirectory)
	if err != nil {
		t.Fatalf("記事APIテスト設定を読み込めません: %v", err)
	}

	openContext, cancelOpen := context.WithTimeout(context.Background(), 15*time.Second)
	store, err := postgres.Open(openContext, cfg.Postgres)
	cancelOpen()
	if err != nil {
		t.Fatalf("PostgreSQLへ接続できません: %v", err)
	}
	defer store.Close()

	server, err := New(
		store,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg.MCP.DefaultArticleLimit,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	requestContext, cancelRequest := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancelRequest()

	sourcesRequest := httptest.NewRequest(http.MethodGet, "/api/sources", nil).
		WithContext(requestContext)
	sourcesRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(sourcesRecorder, sourcesRequest)
	if sourcesRecorder.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/sources 状態コード = %d: %s",
			sourcesRecorder.Code,
			sourcesRecorder.Body.String(),
		)
	}
	var sourcesResponse sourceAPIResponse
	if err := json.Unmarshal(sourcesRecorder.Body.Bytes(), &sourcesResponse); err != nil {
		t.Fatalf("取得対象一覧JSONを解析できません: %v", err)
	}
	for _, source := range sourcesResponse.Sources {
		if source.ID < 1 || source.Name == "" {
			t.Errorf("取得対象 = %+v, 正のIDと設定名を期待", source)
		}
	}

	// ----------------------------------------

	articlesRequest := httptest.NewRequest(http.MethodGet, "/api/articles", nil).
		WithContext(requestContext)
	articlesRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(articlesRecorder, articlesRequest)
	if articlesRecorder.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/articles 状態コード = %d: %s",
			articlesRecorder.Code,
			articlesRecorder.Body.String(),
		)
	}
	var articlesResponse articleAPIResponse
	if err := json.Unmarshal(articlesRecorder.Body.Bytes(), &articlesResponse); err != nil {
		t.Fatalf("記事一覧JSONを解析できません: %v", err)
	}
	if len(articlesResponse.Articles) > cfg.MCP.DefaultArticleLimit {
		t.Errorf(
			"記事件数 = %d, conf既定上限%dを超えています",
			len(articlesResponse.Articles),
			cfg.MCP.DefaultArticleLimit,
		)
	}
	if len(articlesResponse.Articles) == 0 {
		t.Log("保存済み記事がないため、取得対象一覧と無条件検索だけを確認しました")
		return
	}

	// ----------------------------------------

	firstArticle := articlesResponse.Articles[0]
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
		query := url.Values{}
		query.Set("source_id", formatTestInt64(firstArticle.SourceID))
		query.Set("date_from", effectiveAt.Add(time.Second).Format(time.RFC3339Nano))
		query.Set("date_to", effectiveAt.Add(-time.Second).Format(time.RFC3339Nano))
		query.Add("keywords", string(headlineRunes))
		query.Add("keywords", string(bodyRunes))
		query.Set("limit", "1")

		combinedRequest := httptest.NewRequest(
			http.MethodGet,
			"/api/articles?"+query.Encode(),
			nil,
		).WithContext(requestContext)
		combinedRecorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(combinedRecorder, combinedRequest)
		if combinedRecorder.Code != http.StatusOK {
			t.Fatalf(
				"複合条件 GET /api/articles 状態コード = %d: %s",
				combinedRecorder.Code,
				combinedRecorder.Body.String(),
			)
		}
		var combinedResponse articleAPIResponse
		if err := json.Unmarshal(
			combinedRecorder.Body.Bytes(),
			&combinedResponse,
		); err != nil {
			t.Fatalf("複合条件の記事一覧JSONを解析できません: %v", err)
		}
		if len(combinedResponse.Articles) != 1 ||
			combinedResponse.Articles[0].ID != firstArticle.ID {
			t.Errorf(
				"複合条件の記事 = %+v, 記事ID %dを期待",
				combinedResponse.Articles,
				firstArticle.ID,
			)
		}
	}

	// ----------------------------------------

	detailRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/articles/"+formatTestInt64(firstArticle.ID)+"?body_limit=100",
		nil,
	).WithContext(requestContext)
	detailRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(detailRecorder, detailRequest)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/articles/:id 状態コード = %d: %s",
			detailRecorder.Code,
			detailRecorder.Body.String(),
		)
	}
	var detailResponse articleAPIDetailResponse
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detailResponse); err != nil {
		t.Fatalf("記事詳細JSONを解析できません: %v", err)
	}
	if detailResponse.Article.ID != firstArticle.ID {
		t.Errorf(
			"記事詳細ID = %d, 期待値は%d",
			detailResponse.Article.ID,
			firstArticle.ID,
		)
	}
	if len([]rune(detailResponse.Article.BodyText)) > 100 {
		t.Errorf(
			"本文文字数 = %d, 上限100を超えています",
			len([]rune(detailResponse.Article.BodyText)),
		)
	}
}

// ----------------------------------------

/*
formatTestInt64 は、実DBテスト用の正整数を10進文字列へ変換します。

機能:
  - 記事IDまたは取得対象IDをURLへ安全に組み込める10進表記にする

引数:
  - value int64: 文字列化する正整数

返り値:
  - string: 10進表記の整数文字列
*/
func formatTestInt64(value int64) string {
	return fmt.Sprintf("%d", value)
}
