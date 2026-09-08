package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/connector/fisco"
	"github.com/masayoshi4649/NewsStore/internal/connector/reuters"
	"github.com/masayoshi4649/NewsStore/internal/domain"
)

/*
TestParseSourceFormUsesRSSFieldsOnly は、RSSフォームがReuters固有値の影響を受けないことを検証します。

機能:
  - RSS URLとリンク先本文取得設定を保存する
  - 未対応のReuters取得内容が同時送信されてもRSSでは検証しない

引数:
  - test *testing.T: フォーム変換を検証するテスト状態

返り値:
  - なし
*/
func TestParseSourceFormUsesRSSFieldsOnly(test *testing.T) {
	test.Parallel()

	form, source, err := parseTestSourceForm(test, url.Values{
		"name":                   {"RSSテスト"},
		"kind":                   {"rss"},
		"endpoint_url":           {"https://example.com/feed.xml"},
		"enabled":                {"on"},
		"fetch_interval_minutes": {"15"},
		"max_items":              {"20"},
		"fetch_full_article":     {"on"},
		"reuters_target":         {"unsupported"},
	})
	if err != nil {
		test.Fatalf("parseSourceForm() error = %v", err)
	}
	if form.EndpointURL != "https://example.com/feed.xml" ||
		source.EndpointURL != "https://example.com/feed.xml" {
		test.Errorf("RSS URLが一致しません: form=%q source=%q", form.EndpointURL, source.EndpointURL)
	}

	var settings map[string]any
	if err := json.Unmarshal(source.Settings, &settings); err != nil {
		test.Fatalf("Settingsを解析できません: %v", err)
	}
	if settings["fetch_full_article"] != true {
		test.Errorf("fetch_full_articleが保存されていません: %#v", settings)
	}
	if _, exists := settings["section_id"]; exists {
		test.Errorf("RSS設定へReuters section_idが混入しました: %#v", settings)
	}
}

// ----------------------------------------

/*
TestParseSourceFormFixesReutersEndpoint は、Reutersの内部設定が選択内容から固定生成されることを検証します。

機能:
  - 投稿された任意URLを無視してReuters日本語版の固定URLを保存する
  - 取得内容識別子をホワイトリストのsection_idへ変換する
  - websiteとoffsetを利用者入力として保存しない

引数:
  - test *testing.T: Reutersフォーム変換を検証するテスト状態

返り値:
  - なし
*/
func TestParseSourceFormFixesReutersEndpoint(test *testing.T) {
	test.Parallel()

	form, source, err := parseTestSourceForm(test, url.Values{
		"name":                   {"Reuters日本"},
		"kind":                   {"reuters"},
		"endpoint_url":           {"https://untrusted.example/api"},
		"enabled":                {"on"},
		"fetch_interval_minutes": {"30"},
		"max_items":              {"10"},
		"fetch_full_article":     {"on"},
		"reuters_target":         {"japan"},
		"website":                {"untrusted-site"},
		"offset":                 {"999"},
	})
	if err != nil {
		test.Fatalf("parseSourceForm() error = %v", err)
	}
	if form.EndpointURL != reuters.EndpointURL || source.EndpointURL != reuters.EndpointURL {
		test.Errorf(
			"Reuters固定URLが使われていません: form=%q source=%q",
			form.EndpointURL,
			source.EndpointURL,
		)
	}

	var settings map[string]any
	if err := json.Unmarshal(source.Settings, &settings); err != nil {
		test.Fatalf("Settingsを解析できません: %v", err)
	}
	if settings["section_id"] != "/world/japan/" {
		test.Errorf(
			"section_id = %#v, 期待値は %q",
			settings["section_id"],
			"/world/japan/",
		)
	}
	for _, forbidden := range []string{"website", "offset", "fetch_full_article"} {
		if _, exists := settings[forbidden]; exists {
			test.Errorf("Reuters設定へ不要項目 %q が保存されました: %#v", forbidden, settings)
		}
	}
}

// ----------------------------------------

/*
TestParseSourceFormRejectsUnknownReutersTarget は、未定義のReuters取得内容を拒否することを検証します。

機能:
  - POST値を直接改変してホワイトリスト外の取得対象を指定できないことを確認する

引数:
  - test *testing.T: Reuters入力検証を管理するテスト状態

返り値:
  - なし
*/
func TestParseSourceFormRejectsUnknownReutersTarget(test *testing.T) {
	test.Parallel()

	_, _, err := parseTestSourceForm(test, url.Values{
		"name":                   {"不正Reuters"},
		"kind":                   {"reuters"},
		"fetch_interval_minutes": {"15"},
		"max_items":              {"20"},
		"reuters_target":         {"unknown"},
	})
	if err == nil || !strings.Contains(err.Error(), "未対応") {
		test.Fatalf("未対応のReuters取得内容が拒否されません: %v", err)
	}
}

// ----------------------------------------

/*
TestBuildSourceListItemsShowsReutersTarget は、一覧でReutersの記事分野が分かることを検証します。

機能:
  - 固定URLの代わりに保存済みsection_idへ対応する表示名を生成する

引数:
  - test *testing.T: 取得対象一覧の表示変換を検証するテスト状態

返り値:
  - なし
*/
func TestBuildSourceListItemsShowsReutersTarget(test *testing.T) {
	test.Parallel()

	items := buildSourceListItems([]domain.Source{{
		Kind:        domain.SourceKindReuters,
		EndpointURL: reuters.EndpointURL,
		Settings:    json.RawMessage(`{"section_id":"/world/"}`),
	}})
	if len(items) != 1 {
		test.Fatalf("一覧件数 = %d, 期待値は1", len(items))
	}
	if items[0].Description != "Reuters 日本語版 / ワールド" {
		test.Errorf("説明 = %q, 期待値が一致しません", items[0].Description)
	}
}

// ----------------------------------------

/*
TestSourceFormTemplateSeparatesConnectorFields は、種類ごとに固有設定欄が排他的になることを検証します。

機能:
  - RSS、Reuters、Fiscoの選択時に他コネクタの欄をhiddenかつdisabledにする
  - Fiscoには固有の入力項目を表示しない
  - ダークテーマとフォーム切替JavaScriptを読み込む

引数:
  - test *testing.T: テンプレート表示を検証するテスト状態

返り値:
  - なし
*/
func TestSourceFormTemplateSeparatesConnectorFields(test *testing.T) {
	test.Parallel()

	server, err := New(nil, nil, nil)
	if err != nil {
		test.Fatalf("New() error = %v", err)
	}

	testCases := []struct {
		name           string
		kind           domain.SourceKind
		hiddenKinds    []string
		disabledFields []string
		reutersTarget  string
	}{
		{
			name:           "RSS",
			kind:           domain.SourceKindRSS,
			hiddenKinds:    []string{"reuters", "fisco"},
			disabledFields: []string{`id="reuters-target"`},
		},
		{
			name:           "Reuters",
			kind:           domain.SourceKindReuters,
			hiddenKinds:    []string{"rss", "fisco"},
			disabledFields: []string{`id="endpoint-url"`, `name="fetch_full_article"`},
			reutersTarget:  "economy",
		},
		{
			name:           "Fisco",
			kind:           domain.SourceKindFisco,
			hiddenKinds:    []string{"rss", "reuters"},
			disabledFields: []string{`id="endpoint-url"`, `name="fetch_full_article"`, `id="reuters-target"`},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		test.Run(testCase.name, func(test *testing.T) {
			var output strings.Builder
			err := server.template.ExecuteTemplate(&output, "source_form.html", sourceFormPage{
				Base: basePage{Title: "テスト"},
				Form: sourceForm{
					Kind:                 testCase.kind,
					FetchIntervalMinutes: 15,
					MaxItems:             20,
					ReutersTarget:        testCase.reutersTarget,
				},
				ReutersTargets: reuters.Targets(),
			})
			if err != nil {
				test.Fatalf("テンプレートを実行できません: %v", err)
			}

			body := output.String()
			for _, hiddenKind := range testCase.hiddenKinds {
				hiddenMarker := `data-connector-fields="` + hiddenKind + `"` + "\n    hidden"
				if !strings.Contains(body, hiddenMarker) {
					test.Errorf("非選択欄がhiddenではありません: marker=%q", hiddenMarker)
				}
			}
			for _, disabledField := range testCase.disabledFields {
				fieldIndex := strings.Index(body, disabledField)
				if fieldIndex < 0 {
					test.Fatalf("入力項目が見つかりません: %s", disabledField)
				}
				fieldEnd := strings.Index(body[fieldIndex:], ">")
				if fieldEnd < 0 || !strings.Contains(body[fieldIndex:fieldIndex+fieldEnd], "disabled") {
					test.Errorf("非選択欄の入力項目がdisabledではありません: %s", disabledField)
				}
			}
			if testCase.kind == domain.SourceKindFisco {
				if !strings.Contains(body, `<option value="fisco" selected>Fisco</option>`) {
					test.Error("Fiscoの選択状態が反映されていません")
				}
				sectionStart := strings.Index(body, `data-connector-fields="fisco"`)
				if sectionStart < 0 {
					test.Fatal("Fiscoの取得内容説明がありません")
				}
				sectionEnd := strings.Index(body[sectionStart:], "</section>")
				if sectionEnd < 0 {
					test.Fatal("Fiscoの取得内容説明が閉じられていません")
				}
				section := body[sectionStart : sectionStart+sectionEnd]
				for _, forbidden := range []string{"hidden", "<input", "<select", "<textarea"} {
					if strings.Contains(section, forbidden) {
						test.Errorf("Fiscoの説明に不要な属性または入力要素があります: %q", forbidden)
					}
				}
			}
			if !strings.Contains(body, `<html lang="ja" data-theme="dark">`) {
				test.Error("ダークテーマが既定になっていません")
			}
			if !strings.Contains(body, `src="/static/source-form.js"`) {
				test.Error("フォーム切替JavaScriptが読み込まれていません")
			}
		})
	}
}

// ----------------------------------------

/*
TestParseSourceFormFixesFiscoEndpoint は、Fiscoが共通設定だけで保存できることを検証します。

機能:
  - 投稿されたURLとRSS・Reutersの固有項目を無視する
  - 固定URL、共通の最大取得件数、空のJSON設定を保存する
  - 数値入力エラー時もFiscoの再表示内容へ不要な固有設定を残さない

引数:
  - test *testing.T: Fiscoフォーム変換を検証するテスト状態

返り値:
  - なし
*/
func TestParseSourceFormFixesFiscoEndpoint(test *testing.T) {
	test.Parallel()

	for _, maxItems := range []string{"10", "不正な件数"} {
		test.Run(maxItems, func(test *testing.T) {
			form, source, err := parseTestSourceForm(test, url.Values{
				"name":                   {"Fiscoニュース"},
				"kind":                   {"fisco"},
				"endpoint_url":           {"https://untrusted.example/api"},
				"enabled":                {"on"},
				"fetch_interval_minutes": {"30"},
				"max_items":              {maxItems},
				"fetch_full_article":     {"on"},
				"reuters_target":         {"unsupported"},
			})
			if form.EndpointURL != fisco.EndpointURL || form.FetchFullArticle || form.ReutersTarget != "" {
				test.Errorf("Fiscoのフォームへ不要な固有設定が残っています: %#v", form)
			}
			if maxItems != "10" {
				if err == nil || !strings.Contains(err.Error(), "最大取得件数") {
					test.Errorf("最大取得件数の入力エラーが返されません: %v", err)
				}
				return
			}
			if err != nil {
				test.Fatalf("Fiscoのフォームを変換できません: %v", err)
			}
			if source.Kind != domain.SourceKindFisco || source.EndpointURL != fisco.EndpointURL {
				test.Errorf("Fiscoの種別または固定URLが一致しません: %#v", source)
			}
			if source.MaxItems != 10 || source.FetchIntervalMinutes != 30 || !source.Enabled {
				test.Errorf("Fiscoの共通設定が一致しません: %#v", source)
			}
			if string(source.Settings) != "{}" {
				test.Errorf("Fisco設定へ不要な項目が保存されました: %s", source.Settings)
			}
		})
	}
}

// ----------------------------------------

/*
TestSourceToFormRestoresFisco は、保存済みFiscoを固有設定なしで編集できることを検証します。

機能:
  - 編集対象のID、名前、取得間隔、最大取得件数を復元する
  - 保存値にかかわらず固定URLを使用し、他コネクタの固有設定を無視する

引数:
  - test *testing.T: Fisco編集フォームを検証するテスト状態

返り値:
  - なし
*/
func TestSourceToFormRestoresFisco(test *testing.T) {
	test.Parallel()

	form := sourceToForm(domain.Source{
		ID:                   42,
		Name:                 "Fiscoニュース",
		Kind:                 domain.SourceKindFisco,
		EndpointURL:          "https://untrusted.example/api",
		Enabled:              true,
		FetchIntervalMinutes: 30,
		MaxItems:             10,
		Settings:             json.RawMessage(`{"fetch_full_article":true,"section_id":"/world/"}`),
	})
	if form.ID != 42 || form.Name != "Fiscoニュース" || form.Kind != domain.SourceKindFisco ||
		!form.Enabled || form.FetchIntervalMinutes != 30 || form.MaxItems != 10 {
		test.Errorf("Fiscoの共通設定が復元されません: %#v", form)
	}
	if form.EndpointURL != fisco.EndpointURL || form.FetchFullArticle || form.ReutersTarget != "" {
		test.Errorf("Fiscoの編集フォームへ不要な固有設定が残っています: %#v", form)
	}
}

// ----------------------------------------

/*
TestBuildSourceListItemsShowsFisco は、Fisco一覧表示がカテゴリを要求しないことを検証します。

機能:
  - 固有設定なしでFiscoのニュース説明とコネクタ表示名を生成する

引数:
  - test *testing.T: 取得対象一覧の表示変換を検証するテスト状態

返り値:
  - なし
*/
func TestBuildSourceListItemsShowsFisco(test *testing.T) {
	test.Parallel()

	items := buildSourceListItems([]domain.Source{{
		Kind:        domain.SourceKindFisco,
		EndpointURL: fisco.EndpointURL,
		Settings:    json.RawMessage(`{}`),
	}})
	if len(items) != 1 {
		test.Fatalf("一覧件数 = %d, 期待値は1", len(items))
	}
	if items[0].Description != "Fisco ニュース" {
		test.Errorf("Fiscoの一覧説明が一致しません: %q", items[0].Description)
	}
	if label := kindLabel(items[0].Kind); label != "Fisco" {
		test.Errorf("Fiscoの表示名が一致しません: %q", label)
	}
}

// ----------------------------------------

/*
parseTestSourceForm は、テスト用POSTフォームをparseSourceFormへ渡します。

機能:
  - URLエンコードしたフォームをGinコンテキストへ設定する
  - 本番と同じフォーム解析処理を呼び出す

引数:
  - test *testing.T: ヘルパー失敗を報告するテスト状態
  - values url.Values: POSTするフォーム値

返り値:
  - sourceForm: 画面へ再表示するフォーム値
  - domain.Source: 保存用に変換された取得対象
  - error: フォーム値が不正な場合のエラー
*/
func parseTestSourceForm(
	test *testing.T,
	values url.Values,
) (sourceForm, domain.Source, error) {
	test.Helper()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/sources",
		strings.NewReader(values.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	context.Request = request

	return parseSourceForm(context, 0)
}
