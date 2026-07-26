// Package connector は、取得元ごとのコネクタを共通契約で選択する機能を提供します。
package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/masayoshi4649/NewsStore/internal/domain"
)

// Fetcher は、1 つの取得対象から見出しと本文を取得するコネクタ契約です。
type Fetcher interface {
	Fetch(ctx context.Context, source domain.Source) (domain.FetchResult, error)
}

// Registry は、取得対象種別に対応するコネクタを保持します。
type Registry struct {
	fetchers map[domain.SourceKind]Fetcher
}

/*
NewRegistry は、RSS と Reuters のコネクタ一覧を生成します。

機能:
  - 取得対象種別とコネクタ実装を対応付ける

引数:
  - rss Fetcher: RSS/Atom 取得に使用するコネクタ
  - reuters Fetcher: Reuters 取得に使用するコネクタ

返り値:
  - *Registry: 初期化済みのコネクタ一覧
*/
func NewRegistry(rss Fetcher, reuters Fetcher) *Registry {
	return &Registry{
		fetchers: map[domain.SourceKind]Fetcher{
			domain.SourceKindRSS:     rss,
			domain.SourceKindReuters: reuters,
		},
	}
}

// ----------------------------------------

/*
Get は、取得対象種別に対応するコネクタを返します。

機能:
  - 登録済みコネクタを種別から検索する
  - 未対応または未設定のコネクタをエラーにする

引数:
  - kind domain.SourceKind: 検索する取得対象種別

返り値:
  - Fetcher: 種別に対応するコネクタ
  - error: 種別が未対応またはコネクタが nil の場合のエラー
*/
func (r *Registry) Get(kind domain.SourceKind) (Fetcher, error) {
	if r == nil {
		return nil, errors.New("コネクタ一覧が初期化されていません")
	}
	fetcher, ok := r.fetchers[kind]
	if !ok || fetcher == nil {
		return nil, fmt.Errorf("未対応のコネクタ種別です: %q", kind)
	}
	return fetcher, nil
}

// ----------------------------------------

/*
ValidateSource は、管理画面から受け取った取得対象設定を検証します。

機能:
  - 名前、種別、URL、取得間隔、最大件数を検証する
  - settings が JSON オブジェクトであることを確認する

引数:
  - source domain.Source: 検証する取得対象

返り値:
  - error: 設定が不正な場合の検証エラー。すべて有効な場合は nil
*/
func ValidateSource(source domain.Source) error {
	var validationErrors []error

	if strings.TrimSpace(source.Name) == "" {
		validationErrors = append(validationErrors, errors.New("名前は必須です"))
	}
	if source.Kind != domain.SourceKindRSS && source.Kind != domain.SourceKindReuters {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("コネクタ種別は rss または reuters を指定してください: %q", source.Kind),
		)
	}

	endpoint, err := url.Parse(strings.TrimSpace(source.EndpointURL))
	if err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("取得先 URL が不正です: %w", err))
	} else {
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			validationErrors = append(validationErrors, errors.New("取得先 URL は http または https が必要です"))
		}
		if endpoint.Hostname() == "" {
			validationErrors = append(validationErrors, errors.New("取得先 URL にホストが必要です"))
		}
		if endpoint.User != nil {
			validationErrors = append(validationErrors, errors.New("取得先 URL にユーザー情報は指定できません"))
		}
	}

	if source.FetchIntervalMinutes < 1 || source.FetchIntervalMinutes > 10080 {
		validationErrors = append(
			validationErrors,
			errors.New("取得間隔は 1 分以上 10080 分以下で指定してください"),
		)
	}
	if source.MaxItems < 1 || source.MaxItems > 50 {
		validationErrors = append(
			validationErrors,
			errors.New("最大取得件数は 1 件以上 50 件以下で指定してください"),
		)
	}

	settings := source.Settings
	if len(settings) == 0 {
		settings = json.RawMessage(`{}`)
	}
	var settingsObject map[string]any
	if err := json.Unmarshal(settings, &settingsObject); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("コネクタ設定 JSON が不正です: %w", err))
	} else if settingsObject == nil {
		validationErrors = append(validationErrors, errors.New("コネクタ設定は JSON オブジェクトが必要です"))
	}

	return errors.Join(validationErrors...)
}
