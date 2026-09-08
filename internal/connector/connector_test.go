package connector_test

import (
	"testing"

	"github.com/masayoshi4649/NewsStore/internal/connector"
	"github.com/masayoshi4649/NewsStore/internal/connector/fisco"
	"github.com/masayoshi4649/NewsStore/internal/connector/reuters"
	"github.com/masayoshi4649/NewsStore/internal/connector/rss"
	"github.com/masayoshi4649/NewsStore/internal/domain"
)

/*
TestRegistrySelectsConnectors は、各取得対象に対応したコネクタの選択を検証します。

機能:
  - RSS、Reuters、Fisco がそれぞれ登録した実装を返すことを確認する
  - 未登録の種別や未設定の Fisco コネクタを拒否する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestRegistrySelectsConnectors(t *testing.T) {
	t.Parallel()
	rssConnector := rss.New(nil, "", 0)
	reutersConnector := reuters.New(nil, "", 0)
	fiscoConnector := fisco.New(nil, "", 0)
	registry := connector.NewRegistry(rssConnector, reutersConnector, fiscoConnector)
	for kind, expected := range map[domain.SourceKind]connector.Fetcher{
		domain.SourceKindRSS:     rssConnector,
		domain.SourceKindReuters: reutersConnector,
		domain.SourceKindFisco:   fiscoConnector,
	} {
		actual, err := registry.Get(kind)
		if err != nil || actual != expected {
			t.Errorf("種別 %q のコネクタが一致しません: 実装=%T エラー=%v", kind, actual, err)
		}
	}
	if _, err := registry.Get("未対応"); err == nil {
		t.Error("未対応のコネクタ種別が拒否されません")
	}
	if _, err := connector.NewRegistry(rssConnector, reutersConnector, nil).Get(domain.SourceKindFisco); err == nil {
		t.Error("未設定の Fisco コネクタが拒否されません")
	}
}

// ----------------------------------------

/*
TestValidateFiscoSource は、Fisco の共通設定の入力検証を確認します。

機能:
  - カテゴリ設定を持たない Fisco と最大取得件数の両端を許可する
  - 共通の件数制限に違反する値を拒否する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestValidateFiscoSource(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 1, 50, 51} {
		source := domain.Source{
			Name:                 "Fisco",
			Kind:                 domain.SourceKindFisco,
			EndpointURL:          fisco.EndpointURL,
			FetchIntervalMinutes: 15,
			MaxItems:             count,
			Settings:             []byte(`{}`),
		}
		err := connector.ValidateSource(source)
		valid := count >= 1 && count <= 50
		if (err == nil) != valid {
			t.Errorf("最大取得件数 %d の検証結果が不正です: %v", count, err)
		}
	}
}
