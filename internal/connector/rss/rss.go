package rss

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/atom"
	nethtml "golang.org/x/net/html"
)

const (
	defaultUserAgent       = "NewsStore/1.0"
	defaultMaxResponseSize = int64(4 * 1024 * 1024)
	maxSourceItems         = 50
	maxArticleBodyBytes    = 2 * 1024 * 1024
	maxFetchResultBytes    = 32 * 1024 * 1024
	maxErrorBodyPreview    = int64(4 * 1024)
	rssProvider            = "rss"
	feedAcceptHeader       = "application/rss+xml,application/atom+xml,application/xml,text/xml;q=0.9,*/*;q=0.8"
	articleAcceptHeader    = "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8"
)

// HTTPDoer は、RSSフィードと記事ページへのHTTPリクエスト実行に必要な最小インターフェースを表します。
type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Connector は、RSSまたはAtomフィードから見出しと本文を取得するコネクタです。
type Connector struct {
	doer             HTTPDoer
	userAgent        string
	maxResponseBytes int64
}

// settings は、取得元ごとに変更できるRSS固有設定を表します。
type settings struct {
	FetchFullArticle bool `json:"fetch_full_article"`
}

// feedItemFormat は、共通フィード項目から失われる本文形式を表します。
type feedItemFormat struct {
	ContentIsText bool
}

// atomContentTranslator は、Atom固有の本文形式を保持しながら共通フィードへ変換します。
type atomContentTranslator struct {
	itemFormats []feedItemFormat
}

// Translate は、Atomフィードを共通フィードへ変換して項目ごとの本文形式を記録します。
//
// 機能:
//   - Atomエントリーと共通フィード項目の順序を維持して本文形式を記録する
//   - type=textまたは未指定のcontentをリテラルテキストとして識別する
//   - gofeedの既定Atom変換処理を使用して共通フィードを生成する
//
// 引数:
//   - rawFeed (interface{}): gofeedが解析したAtomフィード
//
// 返り値:
//   - *gofeed.Feed: Atomフィードから変換した共通フィード
//   - error: Atomフィード型の確認または共通フィード変換に失敗した場合のエラー。成功時はnil
func (translator *atomContentTranslator) Translate(
	rawFeed interface{},
) (*gofeed.Feed, error) {
	atomFeed, ok := rawFeed.(*atom.Feed)
	if !ok {
		return nil, fmt.Errorf("Atomフィード型が不正です: %T", rawFeed)
	}

	translator.itemFormats = make([]feedItemFormat, len(atomFeed.Entries))
	for index, entry := range atomFeed.Entries {
		if entry == nil || entry.Content == nil {
			continue
		}

		contentType := strings.ToLower(strings.TrimSpace(entry.Content.Type))
		translator.itemFormats[index].ContentIsText =
			contentType == "" || contentType == "text"
	}

	defaultTranslator := &gofeed.DefaultAtomTranslator{}
	return defaultTranslator.Translate(rawFeed)
}

// New は、RSSまたはAtomフィードから記事を取得するコネクタを生成します。
//
// 機能:
//   - HTTPクライアント、User-Agent、レスポンスサイズ上限をコネクタへ設定する
//   - 未指定の値には安全な既定値を設定する
//
// 引数:
//   - doer (HTTPDoer): HTTPリクエストを実行するクライアント。nilの場合はhttp.DefaultClient
//   - userAgent (string): 取得先へ送るUser-Agent。空文字の場合は既定値
//   - maxResponseBytes (int64): 1レスポンス当たりの最大読み取りバイト数。0以下の場合は既定値
//
// 返り値:
//   - *Connector: 初期化済みのRSSコネクタ
func New(doer HTTPDoer, userAgent string, maxResponseBytes int64) *Connector {
	if doer == nil {
		doer = http.DefaultClient
	}

	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseSize
	}

	return &Connector{
		doer:             doer,
		userAgent:        userAgent,
		maxResponseBytes: maxResponseBytes,
	}
}

// ----------------------------------------

// Fetch は、RSSまたはAtomフィードから最大件数までの記事を取得します。
//
// 機能:
//   - フィードURLを検証し、コンテキスト付きGETでフィードを取得する
//   - Source.Settingsに応じてフィード本文またはリンク先の本文を抽出する
//   - 個別記事の不備やリンク先取得失敗を警告へ変換し、残りの記事処理を継続する
//
// 引数:
//   - ctx (context.Context): HTTPリクエストのキャンセルと期限を伝播するコンテキスト
//   - source (domain.Source): フィードURL、最大件数、RSS固有設定を持つ取得元
//
// 返り値:
//   - domain.FetchResult: 取得できた記事と継続可能な警告
//   - error: 入力検証、フィード取得、フィード解析に失敗した場合のエラー。成功時はnil
func (connector *Connector) Fetch(
	ctx context.Context,
	source domain.Source,
) (domain.FetchResult, error) {
	if ctx == nil {
		return domain.FetchResult{}, errors.New("RSS取得コンテキストがnilです")
	}
	if connector == nil {
		return domain.FetchResult{}, errors.New("RSSコネクタがnilです")
	}
	if connector.doer == nil {
		return domain.FetchResult{}, errors.New("RSS HTTPクライアントがnilです")
	}
	if connector.maxResponseBytes <= 0 {
		return domain.FetchResult{}, errors.New("RSSレスポンスサイズ上限が不正です")
	}
	if source.MaxItems < 1 || source.MaxItems > maxSourceItems {
		return domain.FetchResult{}, fmt.Errorf(
			"RSS取得件数max_itemsは1以上%d以下である必要があります: %d",
			maxSourceItems,
			source.MaxItems,
		)
	}

	currentSettings, err := loadSettings(source.Settings)
	if err != nil {
		return domain.FetchResult{}, err
	}

	feedURL, err := validateHTTPURL(source.EndpointURL)
	if err != nil {
		return domain.FetchResult{}, fmt.Errorf("RSS endpoint_urlが不正です: %w", err)
	}

	feedPayload, finalFeedURL, err := connector.fetchResource(
		ctx,
		feedURL.String(),
		feedAcceptHeader,
		"RSSフィード",
	)
	if err != nil {
		return domain.FetchResult{}, fmt.Errorf("RSSフィードの取得に失敗しました: %w", err)
	}

	parser := gofeed.NewParser()
	contentTranslator := &atomContentTranslator{}
	parser.AtomTranslator = contentTranslator
	feed, err := parser.Parse(bytes.NewReader(feedPayload))
	if err != nil {
		return domain.FetchResult{}, fmt.Errorf("RSSフィードの解析に失敗しました: %w", err)
	}
	if feed == nil {
		return domain.FetchResult{}, errors.New("RSSフィードの解析結果がnilです")
	}

	itemCount := len(feed.Items)
	if itemCount > source.MaxItems {
		itemCount = source.MaxItems
	}

	result := domain.FetchResult{
		Articles: make([]domain.Article, 0, itemCount),
		Warnings: make([]string, 0),
	}
	resultBytes := 0
	for index := 0; index < itemCount; index++ {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("RSS記事処理が中断されました: %w", err)
		}

		item := feed.Items[index]
		itemFormat := feedItemFormat{}
		if index < len(contentTranslator.itemFormats) {
			itemFormat = contentTranslator.itemFormats[index]
		}
		article, itemWarnings, include, buildErr := connector.buildArticle(
			ctx,
			source,
			finalFeedURL,
			item,
			index,
			currentSettings,
			itemFormat,
		)
		result.Warnings = append(result.Warnings, itemWarnings...)
		if buildErr != nil {
			if errors.Is(buildErr, context.Canceled) ||
				errors.Is(buildErr, context.DeadlineExceeded) {
				return result, buildErr
			}

			result.Warnings = append(
				result.Warnings,
				fmt.Sprintf("RSS記事の変換に失敗しました: index=%d: %v", index, buildErr),
			)
			continue
		}
		if include {
			articleBytes := len(article.Headline) +
				len(article.BodyText) +
				len(article.RawPayload)
			if resultBytes+articleBytes > maxFetchResultBytes {
				result.Warnings = append(
					result.Warnings,
					fmt.Sprintf(
						"RSS取得結果が総量上限を超えるため残りを省略しました: limit=%d",
						maxFetchResultBytes,
					),
				)
				break
			}
			resultBytes += articleBytes
			result.Articles = append(result.Articles, article)
		}
	}

	return result, nil
}

// ----------------------------------------

// loadSettings は、Source.SettingsからRSS固有設定を読み込みます。
//
// 機能:
//   - 空またはnullのJSONを既定設定として扱う
//   - fetch_full_articleをJSONから読み取る
//
// 引数:
//   - rawSettings (json.RawMessage): RSS固有設定を格納したJSON
//
// 返り値:
//   - settings: 読み取ったRSS固有設定
//   - error: JSONの解析に失敗した場合のエラー。成功時はnil
func loadSettings(rawSettings json.RawMessage) (settings, error) {
	currentSettings := settings{}
	trimmedSettings := bytes.TrimSpace(rawSettings)
	if len(trimmedSettings) == 0 || bytes.Equal(trimmedSettings, []byte("null")) {
		return currentSettings, nil
	}

	if err := json.Unmarshal(trimmedSettings, &currentSettings); err != nil {
		return settings{}, fmt.Errorf("RSS settingsのJSON解析に失敗しました: %w", err)
	}

	return currentSettings, nil
}

// ----------------------------------------

// buildArticle は、フィード項目を共通記事へ変換します。
//
// 機能:
//   - 見出し、本文、URL、日時、ID、未加工項目JSONを共通記事へ割り当てる
//   - fetch_full_article有効時はリンク先本文を優先し、失敗時はフィード本文へ戻す
//   - 見出しまたは本文が空の項目を警告付きで保存対象外にする
//
// 引数:
//   - ctx (context.Context): リンク先取得へ伝播するコンテキスト
//   - source (domain.Source): SourceIDとフィードURLを持つ取得元
//   - feedBaseURL (string): 相対記事リンクの解決に使用する最終フィードURL
//   - item (*gofeed.Item): 変換対象のRSSまたはAtom項目
//   - index (int): 警告へ表示するフィード内の項目位置
//   - currentSettings (settings): RSS固有の本文取得設定
//   - itemFormat (feedItemFormat): Atom固有の本文形式
//
// 返り値:
//   - domain.Article: 変換後の共通記事
//   - []string: 個別項目について発生した継続可能な警告
//   - bool: 記事を取得結果へ含める場合はtrue
//   - error: 処理を継続できない個別変換エラー。成功時はnil
func (connector *Connector) buildArticle(
	ctx context.Context,
	source domain.Source,
	feedBaseURL string,
	item *gofeed.Item,
	index int,
	currentSettings settings,
	itemFormat feedItemFormat,
) (domain.Article, []string, bool, error) {
	if item == nil {
		return domain.Article{}, []string{
			fmt.Sprintf("RSS項目がnilのためスキップしました: index=%d", index),
		}, false, nil
	}

	headline, err := plainTextFromHTML(item.Title)
	if err != nil {
		return domain.Article{}, nil, false, fmt.Errorf("見出しのHTML解析に失敗しました: %w", err)
	}
	if headline == "" {
		return domain.Article{}, []string{
			fmt.Sprintf("RSS記事の見出しが空のためスキップしました: index=%d", index),
		}, false, nil
	}

	warnings := make([]string, 0)
	canonicalURL := ""
	rawLink := strings.TrimSpace(item.Link)
	if rawLink != "" {
		parsedLink, linkErr := resolveHTTPURL(feedBaseURL, rawLink)
		if linkErr != nil {
			warnings = append(
				warnings,
				fmt.Sprintf(
					"RSS記事リンクが不正です: index=%d link=%q: %v",
					index,
					rawLink,
					linkErr,
				),
			)
		} else {
			canonicalURL = parsedLink.String()
		}
	}

	bodyText := ""
	if currentSettings.FetchFullArticle {
		if canonicalURL == "" {
			warnings = append(
				warnings,
				fmt.Sprintf(
					"RSS記事のリンク先本文を取得できないためフィード本文へフォールバックしました: index=%d link=%q",
					index,
					rawLink,
				),
			)
		} else {
			fullArticleBody, fullArticleErr := connector.fetchFullArticleBody(ctx, canonicalURL)
			if fullArticleErr != nil {
				if errors.Is(fullArticleErr, context.Canceled) ||
					errors.Is(fullArticleErr, context.DeadlineExceeded) {
					return domain.Article{}, warnings, false, fullArticleErr
				}

				warnings = append(
					warnings,
					fmt.Sprintf(
						"RSS記事のリンク先本文取得に失敗したためフィード本文へフォールバックしました: index=%d link=%q: %v",
						index,
						canonicalURL,
						fullArticleErr,
					),
				)
			} else {
				bodyText = fullArticleBody
			}
		}
	}

	if bodyText == "" {
		bodyText, err = feedItemBody(item, itemFormat)
		if err != nil {
			return domain.Article{}, warnings, false, fmt.Errorf(
				"フィード本文のHTML解析に失敗しました: %w",
				err,
			)
		}
	}
	if bodyText == "" {
		warnings = append(
			warnings,
			fmt.Sprintf(
				"RSS記事の本文が空のためスキップしました: index=%d headline=%q",
				index,
				headline,
			),
		)
		return domain.Article{}, warnings, false, nil
	}
	if len(bodyText) > maxArticleBodyBytes {
		warnings = append(
			warnings,
			fmt.Sprintf(
				"RSS記事の本文がサイズ上限を超えたためスキップしました: index=%d bytes=%d limit=%d",
				index,
				len(bodyText),
				maxArticleBodyBytes,
			),
		)
		return domain.Article{}, warnings, false, nil
	}

	rawPayload, err := json.Marshal(item)
	if err != nil {
		return domain.Article{}, warnings, false, fmt.Errorf(
			"フィード項目のJSON変換に失敗しました: %w",
			err,
		)
	}

	externalID := buildExternalID(source.EndpointURL, item, headline)
	revisionID := buildRevisionID(item, headline, bodyText)

	return domain.Article{
		SourceID:     source.ID,
		Provider:     rssProvider,
		ExternalID:   externalID,
		RevisionID:   revisionID,
		CanonicalURL: canonicalURL,
		PublishedAt:  cloneTime(item.PublishedParsed),
		UpdatedAt:    cloneTime(item.UpdatedParsed),
		Headline:     headline,
		BodyText:     bodyText,
		RawPayload:   json.RawMessage(rawPayload),
	}, warnings, true, nil
}

// ----------------------------------------

// feedItemBody は、フィード項目内の本文候補を優先順にプレーンテキスト化します。
//
// 機能:
//   - content:encodedまたはAtom contentをdescriptionより優先する
//   - Atom contentがtype=textの場合はHTML解析せずリテラルテキストとして扱う
//   - 上位候補が空文字へ変換された場合は次の候補を試す
//
// 引数:
//   - item (*gofeed.Item): ContentとDescriptionを持つフィード項目
//   - itemFormat (feedItemFormat): Atom固有の本文形式
//
// 返り値:
//   - string: 本文形式に従って空白と段落を正規化した本文
//   - error: HTML解析に失敗した場合のエラー。成功時はnil
func feedItemBody(item *gofeed.Item, itemFormat feedItemFormat) (string, error) {
	if item == nil {
		return "", errors.New("RSS項目がnilです")
	}

	candidates := []struct {
		value  string
		isText bool
	}{
		{
			value:  item.Content,
			isText: itemFormat.ContentIsText,
		},
		{
			value: item.Description,
		},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.value) == "" {
			continue
		}

		bodyText := ""
		if candidate.isText {
			bodyText = normalizeMultilineText(candidate.value)
		} else {
			var err error
			bodyText, err = plainTextFromHTML(candidate.value)
			if err != nil {
				return "", err
			}
		}
		if bodyText != "" {
			return bodyText, nil
		}
	}

	return "", nil
}

// ----------------------------------------

// fetchFullArticleBody は、記事リンク先のHTMLから本文段落を抽出します。
//
// 機能:
//   - 記事URLへAcceptとUser-Agent付きのGETリクエストを送る
//   - article、main、bodyの順で最初に見つかった有効なp段落群を採用する
//
// 引数:
//   - ctx (context.Context): HTTPリクエストへ伝播するコンテキスト
//   - articleURL (string): 本文抽出対象の記事URL
//
// 返り値:
//   - string: 抽出した段落を改行で連結した本文
//   - error: URL検証、HTTP取得、HTML解析、段落抽出に失敗した場合のエラー。成功時はnil
func (connector *Connector) fetchFullArticleBody(
	ctx context.Context,
	articleURL string,
) (string, error) {
	payload, _, err := connector.fetchResource(
		ctx,
		articleURL,
		articleAcceptHeader,
		"RSS記事ページ",
	)
	if err != nil {
		return "", err
	}

	bodyText, err := extractArticleParagraphs(payload)
	if err != nil {
		return "", fmt.Errorf("RSS記事ページのHTML解析に失敗しました: %w", err)
	}
	if bodyText == "" {
		return "", errors.New("RSS記事ページのarticle、main、bodyに有効なp段落がありません")
	}

	return bodyText, nil
}

// ----------------------------------------

// fetchResource は、検証済みHTTP URLからサイズ制限付きでレスポンスを取得します。
//
// 機能:
//   - httpまたはhttpsの絶対URLだけを許可し、userinfoを含むURLを拒否する
//   - コンテキスト、Accept、User-Agentを設定したGETリクエストを実行する
//   - 非2xxステータスとレスポンスサイズ上限超過をエラーにする
//   - リダイレクト後の最終レスポンスURLを検証して返す
//
// 引数:
//   - ctx (context.Context): HTTPリクエストへ伝播するコンテキスト
//   - targetURL (string): 取得対象の絶対URL
//   - accept (string): HTTP Acceptヘッダーへ設定する値
//   - resourceName (string): エラーメッセージへ表示する取得対象名
//
// 返り値:
//   - []byte: サイズ上限内で取得したレスポンス本文
//   - string: リダイレクトを反映して検証した最終レスポンスURL
//   - error: URL検証、HTTP通信、読み取り、ステータス検証に失敗した場合のエラー。成功時はnil
func (connector *Connector) fetchResource(
	ctx context.Context,
	targetURL string,
	accept string,
	resourceName string,
) ([]byte, string, error) {
	parsedURL, err := validateHTTPURL(targetURL)
	if err != nil {
		return nil, "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("%sのHTTPリクエスト作成に失敗しました: %w", resourceName, err)
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", connector.userAgent)

	response, err := connector.doer.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("%sへのHTTPリクエストに失敗しました: %w", resourceName, err)
	}
	if response == nil {
		return nil, "", fmt.Errorf("%sからnilレスポンスが返されました", resourceName)
	}
	if response.Body == nil {
		return nil, "", fmt.Errorf("%sレスポンスのbodyがnilです", resourceName)
	}
	defer response.Body.Close()

	finalURL := parsedURL.String()
	if response.Request != nil && response.Request.URL != nil {
		validatedFinalURL, finalURLErr := validateHTTPURL(response.Request.URL.String())
		if finalURLErr != nil {
			return nil, "", fmt.Errorf(
				"%sの最終レスポンスURLが不正です: %w",
				resourceName,
				finalURLErr,
			)
		}
		finalURL = validatedFinalURL.String()
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		preview, exceeded, previewErr := readBoundedBody(response.Body, maxErrorBodyPreview)
		if previewErr != nil {
			return nil, "", fmt.Errorf("%sエラーレスポンスを読み取れません: %w", resourceName, previewErr)
		}
		truncated := ""
		if exceeded {
			truncated = "（プレビュー上限で省略）"
		}
		return nil, "", fmt.Errorf(
			"%sが非2xxで終了しました: status=%d body=%q%s",
			resourceName,
			response.StatusCode,
			string(preview),
			truncated,
		)
	}

	payload, exceeded, err := readBoundedBody(response.Body, connector.maxResponseBytes)
	if err != nil {
		return nil, "", fmt.Errorf("%sレスポンスの読み取りに失敗しました: %w", resourceName, err)
	}
	if exceeded {
		return nil, "", fmt.Errorf(
			"%sレスポンスがサイズ上限を超えました: max_response_bytes=%d",
			resourceName,
			connector.maxResponseBytes,
		)
	}

	return payload, finalURL, nil
}

// ----------------------------------------

// validateHTTPURL は、外部取得に使用できるHTTP URLかを検証します。
//
// 機能:
//   - URLを解析してhttpまたはhttpsの絶対URLであることを確認する
//   - hostが空のURLとuserinfoを含むURLを拒否する
//
// 引数:
//   - rawURL (string): 検証対象のURL文字列
//
// 返り値:
//   - *url.URL: 検証済みのURL
//   - error: URLが外部取得条件を満たさない場合のエラー。成功時はnil
func validateHTTPURL(rawURL string) (*url.URL, error) {
	trimmedURL := strings.TrimSpace(rawURL)
	if trimmedURL == "" {
		return nil, errors.New("URLが空です")
	}

	parsedURL, err := url.Parse(trimmedURL)
	if err != nil {
		return nil, fmt.Errorf("URLの解析に失敗しました: %w", err)
	}

	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("URL schemeはhttpまたはhttpsである必要があります: %q", parsedURL.Scheme)
	}
	if parsedURL.Host == "" {
		return nil, errors.New("URL hostが空です")
	}
	if parsedURL.Hostname() == "" {
		return nil, errors.New("URL hostnameが空です")
	}
	if parsedURL.User != nil {
		return nil, errors.New("userinfoを含むURLは使用できません")
	}
	if parsedURL.Opaque != "" {
		return nil, errors.New("opaque形式のURLは使用できません")
	}

	parsedURL.Scheme = scheme
	return parsedURL, nil
}

// ----------------------------------------

// resolveHTTPURL は、フィード基準URLに対して記事リンクを解決して検証します。
//
// 機能:
//   - 絶対リンクはそのままHTTP URL検証へ渡す
//   - 相対リンクは検証済みフィードURLを基準に絶対URLへ解決する
//   - 解決後もhttpまたはhttps、host、userinfoの条件を検証する
//
// 引数:
//   - baseURL (string): 相対リンク解決の基準となるフィードURL
//   - rawReference (string): RSSまたはAtom項目に記載された記事リンク
//
// 返り値:
//   - *url.URL: 絶対URLへ解決して検証した記事URL
//   - error: 基準URL、リンク、解決後URLが条件を満たさない場合のエラー。成功時はnil
func resolveHTTPURL(baseURL string, rawReference string) (*url.URL, error) {
	parsedBaseURL, err := validateHTTPURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("基準URLが不正です: %w", err)
	}

	trimmedReference := strings.TrimSpace(rawReference)
	if trimmedReference == "" {
		return nil, errors.New("記事リンクが空です")
	}

	referenceURL, err := url.Parse(trimmedReference)
	if err != nil {
		return nil, fmt.Errorf("記事リンクの解析に失敗しました: %w", err)
	}
	if referenceURL.IsAbs() {
		return validateHTTPURL(referenceURL.String())
	}

	resolvedURL := parsedBaseURL.ResolveReference(referenceURL)
	return validateHTTPURL(resolvedURL.String())
}

// ----------------------------------------

// readBoundedBody は、指定したサイズ上限までHTTPレスポンス本文を読み取ります。
//
// 機能:
//   - 上限より1バイト多く読み、レスポンス超過を検出する
//   - 上限を超えた場合も返却本文を上限以内へ切り詰める
//
// 引数:
//   - reader (io.Reader): 読み取り対象のHTTPレスポンス本文
//   - maxBytes (int64): 返却可能な最大バイト数
//
// 返り値:
//   - []byte: 最大maxBytesまでのレスポンス本文
//   - bool: 本文がmaxBytesを超えた場合はtrue
//   - error: 引数検証または本文読み取りに失敗した場合のエラー。成功時はnil
func readBoundedBody(reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	if reader == nil {
		return nil, false, errors.New("レスポンス本文のreaderがnilです")
	}
	if maxBytes <= 0 {
		return nil, false, fmt.Errorf("レスポンスサイズ上限は1以上である必要があります: %d", maxBytes)
	}

	payload, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(payload)) <= maxBytes {
		return payload, false, nil
	}

	return payload[:maxBytes], true, nil
}

// ----------------------------------------

// extractArticleParagraphs は、HTML文書から優先要素内のp段落を抽出します。
//
// 機能:
//   - HTMLを寛容なHTML5パーサーで解析する
//   - article、main、bodyの順に要素を探し、有効なp段落を改行で連結する
//
// 引数:
//   - payload ([]byte): 解析対象のHTML文書
//
// 返り値:
//   - string: 優先要素から抽出したプレーンテキスト本文
//   - error: HTML解析に失敗した場合のエラー。成功時はnil
func extractArticleParagraphs(payload []byte) (string, error) {
	document, err := nethtml.Parse(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}

	for _, tagName := range []string{"article", "main", "body"} {
		scopes := findElementsByTag(document, tagName)
		for _, scope := range scopes {
			paragraphs := collectParagraphs(scope)
			if len(paragraphs) > 0 {
				return strings.Join(paragraphs, "\n"), nil
			}
		}
	}

	return "", nil
}

// ----------------------------------------

// findElementsByTag は、HTMLノード配下から指定タグの要素を出現順に探します。
//
// 機能:
//   - 深さ優先で子孫ノードを走査する
//   - タグ名が一致したすべての要素ノードを返す
//
// 引数:
//   - root (*html.Node): 検索を開始するルートノード
//   - tagName (string): 小文字で指定する検索対象タグ名
//
// 返り値:
//   - []*html.Node: 出現順の一致要素一覧
func findElementsByTag(root *nethtml.Node, tagName string) []*nethtml.Node {
	elements := make([]*nethtml.Node, 0)
	if root == nil {
		return elements
	}

	if root.Type == nethtml.ElementNode && strings.EqualFold(root.Data, tagName) {
		elements = append(elements, root)
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		elements = append(elements, findElementsByTag(child, tagName)...)
	}

	return elements
}

// ----------------------------------------

// collectParagraphs は、HTML要素配下のp段落をプレーンテキストとして収集します。
//
// 機能:
//   - 子孫のp要素を出現順に走査する
//   - 空の段落とスクリプトなど表示対象外の内容を除外する
//
// 引数:
//   - root (*html.Node): 段落を検索する要素ノード
//
// 返り値:
//   - []string: 空要素を除いたプレーンテキスト段落一覧
func collectParagraphs(root *nethtml.Node) []string {
	paragraphs := make([]string, 0)
	if root == nil {
		return paragraphs
	}

	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == nethtml.ElementNode && strings.EqualFold(child.Data, "p") {
			paragraph := textFromNode(child)
			if paragraph != "" {
				paragraphs = append(paragraphs, paragraph)
			}
			continue
		}

		paragraphs = append(paragraphs, collectParagraphs(child)...)
	}

	return paragraphs
}

// ----------------------------------------

// plainTextFromHTML は、HTML断片を表示用のプレーンテキストへ変換します。
//
// 機能:
//   - HTML5パーサーで断片を解析してタグを除去する
//   - 段落やブロック要素の境界を改行として保持し、空白を正規化する
//
// 引数:
//   - sourceHTML (string): 変換対象のHTMLまたはプレーンテキスト
//
// 返り値:
//   - string: HTMLタグを除去して空白を正規化した文字列
//   - error: HTML断片の解析に失敗した場合のエラー。成功時はnil
func plainTextFromHTML(sourceHTML string) (string, error) {
	nodes, err := nethtml.ParseFragment(strings.NewReader(sourceHTML), nil)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	for _, node := range nodes {
		appendVisibleText(&builder, node)
	}

	return normalizeMultilineText(builder.String()), nil
}

// ----------------------------------------

// textFromNode は、HTMLノード配下の表示対象テキストを抽出します。
//
// 機能:
//   - 指定ノード配下のテキストと改行境界を文字列へ集約する
//   - 行内空白と空行を正規化する
//
// 引数:
//   - node (*html.Node): テキスト抽出対象のHTMLノード
//
// 返り値:
//   - string: 正規化済みのプレーンテキスト
func textFromNode(node *nethtml.Node) string {
	var builder strings.Builder
	appendVisibleText(&builder, node)
	return normalizeMultilineText(builder.String())
}

// ----------------------------------------

// appendVisibleText は、HTMLノードの表示対象テキストを文字列ビルダーへ追加します。
//
// 機能:
//   - script、style、noscript、templateの内容を除外する
//   - brと主要ブロック要素を改行へ変換する
//   - 子孫ノードを出現順に再帰走査する
//
// 引数:
//   - builder (*strings.Builder): 抽出したテキストの追加先
//   - node (*html.Node): 走査対象のHTMLノード
//
// 返り値:
//   - なし
func appendVisibleText(builder *strings.Builder, node *nethtml.Node) {
	if builder == nil || node == nil {
		return
	}

	if node.Type == nethtml.TextNode {
		builder.WriteString(node.Data)
		return
	}
	if node.Type == nethtml.ElementNode {
		tagName := strings.ToLower(node.Data)
		switch tagName {
		case "script", "style", "noscript", "template":
			return
		case "br":
			builder.WriteByte('\n')
			return
		}

		if isBlockElement(tagName) {
			builder.WriteByte('\n')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			appendVisibleText(builder, child)
		}
		if isBlockElement(tagName) {
			builder.WriteByte('\n')
		}
		return
	}

	for child := node.FirstChild; child != nil; child = child.NextSibling {
		appendVisibleText(builder, child)
	}
}

// ----------------------------------------

// isBlockElement は、本文抽出時に改行境界として扱うHTMLタグかを判定します。
//
// 機能:
//   - ニュース本文で使用される主要なブロック要素を判定する
//
// 引数:
//   - tagName (string): 小文字へ正規化済みのHTMLタグ名
//
// 返り値:
//   - bool: ブロック要素として改行を挿入する場合はtrue
func isBlockElement(tagName string) bool {
	switch tagName {
	case "address",
		"article",
		"aside",
		"blockquote",
		"div",
		"dl",
		"fieldset",
		"figcaption",
		"figure",
		"footer",
		"form",
		"h1",
		"h2",
		"h3",
		"h4",
		"h5",
		"h6",
		"header",
		"hr",
		"li",
		"main",
		"nav",
		"ol",
		"p",
		"pre",
		"section",
		"table",
		"tbody",
		"td",
		"tfoot",
		"th",
		"thead",
		"tr",
		"ul":
		return true
	default:
		return false
	}
}

// ----------------------------------------

// normalizeMultilineText は、行構造を保ちながら空白を正規化します。
//
// 機能:
//   - CRLFとCRをLFへ統一する
//   - 各行の連続空白を単一空白へ変換し、空行を除外する
//
// 引数:
//   - value (string): 正規化対象の文字列
//
// 返り値:
//   - string: 空白と改行を正規化した文字列
func normalizeMultilineText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")

	rawLines := strings.Split(value, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, rawLine := range rawLines {
		line := strings.Join(strings.Fields(rawLine), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, "\n")
}

// ----------------------------------------

// buildExternalID は、フィード項目の安定した外部IDを生成します。
//
// 機能:
//   - GUID、Linkの順で空でない識別子を採用する
//   - 両方がない場合はフィードURL、見出し、公開日時のSHA-256を採用する
//
// 引数:
//   - feedURL (string): 項目を取得したフィードURL
//   - item (*gofeed.Item): GUID、Link、公開日時を持つフィード項目
//   - headline (string): 正規化済みの記事見出し
//
// 返り値:
//   - string: 保存と重複判定に使用する安定した外部ID
func buildExternalID(feedURL string, item *gofeed.Item, headline string) string {
	if item != nil {
		if guid := strings.TrimSpace(item.GUID); guid != "" {
			return guid
		}
		if link := strings.TrimSpace(item.Link); link != "" {
			return link
		}
	}

	published := ""
	if item != nil {
		published = stableTimeValue(item.PublishedParsed, item.Published)
	}

	return hashParts("rss-external-id", strings.TrimSpace(feedURL), headline, published)
}

// ----------------------------------------

// buildRevisionID は、記事内容を表すリビジョンIDを生成します。
//
// 機能:
//   - 見出し、本文、更新日時を境界付きでSHA-256へ入力する
//   - 同じ入力から常に同じ小文字16進IDを生成する
//
// 引数:
//   - item (*gofeed.Item): 更新日時を持つフィード項目
//   - headline (string): 正規化済みの記事見出し
//   - bodyText (string): 正規化済みの記事本文
//
// 返り値:
//   - string: 記事内容を識別するSHA-256リビジョンID
func buildRevisionID(item *gofeed.Item, headline string, bodyText string) string {
	updated := ""
	if item != nil {
		updated = stableTimeValue(item.UpdatedParsed, item.Updated)
	}

	return hashParts("rss-revision-id", headline, bodyText, updated)
}

// ----------------------------------------

// stableTimeValue は、日時をハッシュ入力向けの安定した文字列へ変換します。
//
// 機能:
//   - 解析済み日時がある場合はUTCのRFC3339Nano形式を優先する
//   - 解析済み日時がない場合は前後空白を除いた元文字列を返す
//
// 引数:
//   - parsed (*time.Time): フィードパーサーが解析した日時
//   - raw (string): フィードに記載された元の日時文字列
//
// 返り値:
//   - string: ハッシュ入力に使用できる安定した日時文字列
func stableTimeValue(parsed *time.Time, raw string) string {
	if parsed != nil {
		return parsed.UTC().Format(time.RFC3339Nano)
	}

	return strings.TrimSpace(raw)
}

// ----------------------------------------

// hashParts は、複数文字列を境界の衝突が起きない形式でSHA-256へ変換します。
//
// 機能:
//   - 各文字列のバイト長と内容を順にハッシュへ入力する
//   - SHA-256値を小文字16進文字列として返す
//
// 引数:
//   - parts (...string): 順序を保持してハッシュ化する文字列群
//
// 返り値:
//   - string: 64文字のSHA-256小文字16進文字列
func hashParts(parts ...string) string {
	hasher := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hasher, "%d:", len(part))
		_, _ = io.WriteString(hasher, part)
		_, _ = io.WriteString(hasher, ";")
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

// ----------------------------------------

// cloneTime は、日時ポインターを記事モデル専用の値として複製します。
//
// 機能:
//   - nilをそのまま保持する
//   - 非nilの日時値をコピーして新しいポインターを返す
//
// 引数:
//   - value (*time.Time): 複製対象の日時
//
// 返り値:
//   - *time.Time: 複製した日時。引数がnilの場合はnil
func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}

	cloned := *value
	return &cloned
}
