package fisco

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/masayoshi4649/NewsStore/internal/netpolicy"
)

/*
TestLiveFiscoEndpoint は、Fiscoの記事一覧と詳細APIへ実通信できることを確認します。

機能:
  - NEWSSTORE_TEST_FISCO_LIVE=1が指定された場合だけ外部APIテストを実行する
  - 本番と同じネットワーク制御で固定URLから最新記事を1件取得する
  - 見出し、本文、取得元、公開日時を検証し、記事内容はログへ出力しない

引数:
  - test *testing.T: 実通信テストの状態を管理する値

返り値:
  - なし
*/
func TestLiveFiscoEndpoint(test *testing.T) {
	if os.Getenv("NEWSSTORE_TEST_FISCO_LIVE") != "1" {
		test.Skip("Fisco実通信テストはNEWSSTORE_TEST_FISCO_LIVE=1の場合だけ実行します")
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: netpolicy.NewTransport(false),
	}
	defer client.CloseIdleConnections()
	connector := New(client, "NewsStore/0.1", 8*1024*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ----------------------------------------

	result, err := connector.Fetch(ctx, domain.Source{
		ID:          1,
		Kind:        domain.SourceKindFisco,
		EndpointURL: EndpointURL,
		MaxItems:    1,
		Settings:    []byte(`{}`),
	})
	if err != nil {
		test.Fatalf("Fisco実通信に失敗しました: %v", err)
	}
	if len(result.Articles) != 1 {
		test.Fatalf("Fisco記事を1件取得できませんでした: articles=%d warnings=%v",
			len(result.Articles), result.Warnings)
	}
	if len(result.Warnings) != 0 {
		test.Fatalf("Fisco記事の取得で警告が発生しました: %v", result.Warnings)
	}

	// ----------------------------------------

	article := result.Articles[0]
	if article.Headline == "" || article.BodyText == "" {
		test.Errorf("Fiscoの見出しまたは本文が空です: headline_length=%d body_length=%d",
			len(article.Headline), len(article.BodyText))
	}
	if article.Provider != "fisco" {
		test.Errorf("Fisco記事の取得元が一致しません: provider=%q", article.Provider)
	}
	if article.PublishedAt == nil || article.PublishedAt.IsZero() {
		test.Error("Fisco記事の公開日時が設定されていません")
	} else if _, offset := article.PublishedAt.Zone(); offset != 9*60*60 {
		test.Errorf("Fisco記事の公開日時が日本時間ではありません: offset=%d", offset)
	}
}
