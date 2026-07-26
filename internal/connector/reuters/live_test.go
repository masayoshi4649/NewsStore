package reuters

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
TestLiveReutersEndpoint は、Reuters日本語版の一覧APIと詳細APIへ実通信できることを確認します。

機能:
  - NEWSSTORE_TEST_REUTERS_LIVE=1が指定された場合だけ外部APIテストを実行する
  - 本番と同じネットワーク制御、固定URL、固定API設定で見出しと本文を取得する

引数:
  - test *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveReutersEndpoint(test *testing.T) {
	if os.Getenv("NEWSSTORE_TEST_REUTERS_LIVE") != "1" {
		test.Skip("Reuters実通信テストはNEWSSTORE_TEST_REUTERS_LIVE=1の場合だけ実行します")
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: netpolicy.NewTransport(false),
	}
	connector := New(client, "NewsStore/0.1", 8*1024*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := connector.Fetch(ctx, domain.Source{
		ID:          1,
		Kind:        domain.SourceKindReuters,
		EndpointURL: "https://example.invalid/この値は使用しない",
		MaxItems:    1,
		Settings:    []byte(`{"section_id":"/economy/"}`),
	})
	if err != nil {
		test.Fatalf("Reuters実通信に失敗しました: %v", err)
	}
	if len(result.Articles) != 1 {
		test.Fatalf(
			"Reuters記事を取得できませんでした: articles=%d warnings=%v",
			len(result.Articles),
			result.Warnings,
		)
	}
	if result.Articles[0].Headline == "" || result.Articles[0].BodyText == "" {
		test.Fatalf(
			"Reutersの見出しまたは本文が空です: headline=%q body_length=%d",
			result.Articles[0].Headline,
			len(result.Articles[0].BodyText),
		)
	}
}

// ----------------------------------------

/*
TestLiveReutersTargets は、定義済みの全取得対象がReuters一覧APIで有効なことを確認します。

機能:
  - NEWSSTORE_TEST_REUTERS_LIVE=1が指定された場合だけ外部APIテストを実行する
  - 管理画面へ表示する全section_idで一覧APIが成功することを確認する

引数:
  - test *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveReutersTargets(test *testing.T) {
	if os.Getenv("NEWSSTORE_TEST_REUTERS_LIVE") != "1" {
		test.Skip("Reuters実通信テストはNEWSSTORE_TEST_REUTERS_LIVE=1の場合だけ実行します")
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: netpolicy.NewTransport(false),
	}
	connector := New(client, "NewsStore/0.1", 8*1024*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, target := range Targets() {
		target := target
		test.Run(target.ID, func(test *testing.T) {
			articles, err := connector.fetchSection(ctx, EndpointURL, settings{
				SectionID: target.SectionID,
				Website:   WebsiteID,
				Offset:    0,
			}, 1)
			if err != nil {
				test.Fatalf(
					"Reuters一覧APIに失敗しました: target=%q section_id=%q error=%v",
					target.ID,
					target.SectionID,
					err,
				)
			}
			if len(articles) == 0 {
				test.Fatalf(
					"Reuters一覧APIの記事が空です: target=%q section_id=%q",
					target.ID,
					target.SectionID,
				)
			}
		})
	}
}
