package reuters

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
)

const (
	// EndpointURL は、Reuters日本語版の固定ベースURLです。
	EndpointURL = "https://jp.reuters.com"
	// WebsiteID は、Reuters日本語版APIの固定サイト識別子です。
	WebsiteID = "reuters-japan"
	// DefaultTargetID は、取得内容が未指定の場合に使う対象識別子です。
	DefaultTargetID = "economy"

	defaultSectionID       = "/economy/"
	japanSectionID         = "/world/japan/"
	legacyJapanSectionID   = "/ja/"
	defaultWebsite         = WebsiteID
	defaultUserAgent       = "NewsStore/1.0"
	defaultMaxResponseSize = int64(4 * 1024 * 1024)
	maxSourceItems         = 50
	maxArticleBodyBytes    = 2 * 1024 * 1024
	maxRawPayloadBytes     = 512 * 1024
	maxFetchResultBytes    = 32 * 1024 * 1024
	maxErrorBodyPreview    = int64(4 * 1024)
	sectionFetchSource     = "articles-by-section-alias-or-id-v1"
	articleFetchSource     = "article-by-id-or-url-v1"
	reutersProvider        = "reuters"
	acceptHeader           = "application/json,text/plain,*/*"
)

// Target は、管理画面から選択できるReutersの記事取得対象を表します。
type Target struct {
	ID        string
	Label     string
	SectionID string
}

var supportedTargets = [...]Target{
	{ID: "economy", Label: "経済", SectionID: "/economy/"},
	{ID: "japan", Label: "日本", SectionID: japanSectionID},
	{ID: "world", Label: "ワールド", SectionID: "/world/"},
	{ID: "europe", Label: "欧州", SectionID: "/world/europe/"},
	{ID: "ukraine", Label: "ウクライナ情勢", SectionID: "/world/ukraine/"},
	{ID: "russia", Label: "ロシア", SectionID: "/world/russia/"},
	{ID: "mideast", Label: "中東・アフリカ", SectionID: "/world/mideast/"},
	{ID: "security", Label: "特集 安全保障問題", SectionID: "/world/security/"},
}

// Doer は、Reuters APIへのHTTPリクエスト実行に必要な最小インターフェースを表します。
type Doer interface {
	Do(request *http.Request) (*http.Response, error)
}

// RevisionChecker は、保存済みReutersリビジョンの存在確認を表します。
type RevisionChecker interface {
	ArticleRevisionExists(
		ctx context.Context,
		sourceID int64,
		externalID string,
		revisionID string,
	) (bool, error)
}

// Connector は、Reutersの記事一覧と記事詳細を取得するコネクタです。
type Connector struct {
	client           Doer
	endpointURL      string
	userAgent        string
	maxResponseBytes int64
	revisionChecker  RevisionChecker
}

// settings は、取得元ごとに上書きできるReuters APIの設定を表します。
type settings struct {
	SectionID string `json:"section_id"`
	Website   string `json:"website"`
	Offset    int    `json:"offset"`
}

// sectionQuery は、記事一覧取得APIのqueryパラメータへ格納するJSONを表します。
type sectionQuery struct {
	ArcSite   string `json:"arc-site"`
	FetchType string `json:"fetch_type"`
	Offset    int    `json:"offset"`
	SectionID string `json:"section_id"`
	Size      int    `json:"size"`
	Website   string `json:"website"`
}

// articleQuery は、記事詳細取得APIのqueryパラメータへ格納するJSONを表します。
type articleQuery struct {
	ID         string `json:"id,omitempty"`
	Website    string `json:"website"`
	WebsiteURL string `json:"website_url,omitempty"`
}

// sectionResponse は、Reutersの記事一覧取得APIレスポンスを表します。
type sectionResponse struct {
	StatusCode int            `json:"statusCode"`
	Message    string         `json:"message"`
	Result     *sectionResult `json:"result"`
}

// sectionResult は、Reutersの記事一覧取得APIレスポンス内のresultを表します。
type sectionResult struct {
	Articles []articleSummary `json:"articles"`
}

// articleSummary は、記事詳細取得に必要なReutersの記事概要を表します。
type articleSummary struct {
	ID            string `json:"id"`
	RevisionID    string `json:"revision_id"`
	Revision      string `json:"revision"`
	Title         string `json:"title"`
	BasicHeadline string `json:"basic_headline"`
	CanonicalURL  string `json:"canonical_url"`
}

// articleDetailResponse は、Reutersの記事詳細取得APIレスポンスを表します。
type articleDetailResponse struct {
	StatusCode int            `json:"statusCode"`
	Message    string         `json:"message"`
	Result     *articleDetail `json:"result"`
}

// articleDetail は、Reutersの記事詳細と本文構成要素を表します。
type articleDetail struct {
	ID              string           `json:"id"`
	RevisionID      string           `json:"revision_id"`
	Title           string           `json:"title"`
	CanonicalURL    string           `json:"canonical_url"`
	PublishedTime   string           `json:"published_time"`
	UpdatedTime     string           `json:"updated_time"`
	Dateline        []string         `json:"dateline"`
	ContentElements []contentElement `json:"content_elements"`
}

// contentElement は、Reutersの記事本文を構成する個別要素を表します。
type contentElement struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ----------------------------------------

/*
Targets は、管理画面で選択できるReutersの記事取得対象を返します。

機能:
  - 現行Reuters一覧APIで取得確認したセクションを列挙する
  - 呼び出し元から内部定義を変更されないようコピーを返す

引数:
  - なし

返り値:
  - []Target: 表示名、識別子、Reuters section_idを持つ取得対象一覧
*/
func Targets() []Target {
	targets := make([]Target, len(supportedTargets))
	copy(targets, supportedTargets[:])
	return targets
}

// ----------------------------------------

/*
FindTarget は、画面用の取得対象識別子からReuters設定を検索します。

機能:
  - 前後空白を除去した識別子をホワイトリストから検索する

引数:
  - id string: 管理画面から送信された取得対象識別子

返り値:
  - Target: 一致したReuters取得対象
  - bool: 一致する取得対象が存在する場合はtrue
*/
func FindTarget(id string) (Target, bool) {
	id = strings.TrimSpace(id)
	for _, target := range supportedTargets {
		if target.ID == id {
			return target, true
		}
	}
	return Target{}, false
}

// ----------------------------------------

/*
FindTargetBySectionID は、保存済みsection_idから画面用の取得対象を検索します。

機能:
  - 既存DB設定を現在のホワイトリストへ対応付ける
  - 旧Reuters taxonomyの日本セクション /ja/ を現行 /world/japan/ へ読み替える

引数:
  - sectionID string: Reuters settingsに保存されたsection_id

返り値:
  - Target: 一致したReuters取得対象
  - bool: 一致する取得対象が存在する場合はtrue
*/
func FindTargetBySectionID(sectionID string) (Target, bool) {
	sectionID = strings.TrimSpace(sectionID)
	if sectionID == legacyJapanSectionID {
		sectionID = japanSectionID
	}
	for _, target := range supportedTargets {
		if target.SectionID == sectionID {
			return target, true
		}
	}
	return Target{}, false
}

// ----------------------------------------

// New は、Reuters APIから記事を取得するコネクタを生成します。
//
// 機能:
//   - HTTPクライアント、User-Agent、レスポンスサイズ上限をコネクタへ設定する
//   - 未指定の値には安全な既定値を設定する
//
// 引数:
//   - client (Doer): HTTPリクエストを実行するクライアント。nilの場合はhttp.DefaultClient
//   - userAgent (string): Reuters APIへ送るUser-Agent。空文字の場合は既定値
//   - maxResponseBytes (int64): 1レスポンス当たりの最大読み取りバイト数。0以下の場合は既定値
//
// 返り値:
//   - *Connector: 初期化済みのReutersコネクタ
func New(client Doer, userAgent string, maxResponseBytes int64) *Connector {
	return newConnector(EndpointURL, client, userAgent, maxResponseBytes)
}

// ----------------------------------------

/*
newConnector は、指定したベースURLを使うReutersコネクタを生成します。

機能:
  - 本番では固定Reuters URL、単体テストではローカルHTTPサーバーを注入できるようにする
  - HTTPクライアント、User-Agent、レスポンスサイズ上限へ既定値を補完する

引数:
  - endpointURL string: Reuters APIとして使用する固定ベースURL
  - client Doer: HTTPリクエストを実行するクライアント
  - userAgent string: Reuters APIへ送るUser-Agent
  - maxResponseBytes int64: 1レスポンス当たりの最大読み取りバイト数

返り値:
  - *Connector: 指定URLを使用する初期化済みReutersコネクタ
*/
func newConnector(
	endpointURL string,
	client Doer,
	userAgent string,
	maxResponseBytes int64,
) *Connector {
	if client == nil {
		client = http.DefaultClient
	}

	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseSize
	}

	return &Connector{
		client:           client,
		endpointURL:      strings.TrimSpace(endpointURL),
		userAgent:        userAgent,
		maxResponseBytes: maxResponseBytes,
	}
}

// ----------------------------------------

// SetRevisionChecker は、詳細APIの再取得を省略する保存済み確認機能を設定します。
//
// 機能:
//   - 一覧のIDとリビジョンが保存済みの場合に詳細取得を省略できるようにする
//   - nil指定時は従来どおり全記事の詳細を取得する
//
// 引数:
//   - checker (RevisionChecker): 保存済み記事リビジョンの確認機能
//
// 返り値:
//   - なし
func (connector *Connector) SetRevisionChecker(checker RevisionChecker) {
	if connector != nil {
		connector.revisionChecker = checker
	}
}

// ----------------------------------------

// Fetch は、Reutersの記事一覧を取得し、各記事の詳細を順に取得します。
//
// 機能:
//   - コネクタに定義された固定URLとSource.Settingsの記事分野で一覧APIを呼び出す
//   - 一覧の各記事について記事詳細APIを呼び出す
//   - 個別記事の取得失敗を警告へ変換し、残りの記事取得を継続する
//   - コンテキスト中断時は残りの記事取得を行わず直ちに終了する
//
// 引数:
//   - ctx (context.Context): HTTPリクエストのキャンセルと期限を伝播するコンテキスト
//   - source (domain.Source): Reuters取得元のID、件数、記事分野設定
//
// 返り値:
//   - domain.FetchResult: 取得できた記事と個別記事に関する警告
//   - error: 設定、記事一覧取得、処理中断により継続できない場合のエラー。成功時はnil
func (connector *Connector) Fetch(ctx context.Context, source domain.Source) (domain.FetchResult, error) {
	if ctx == nil {
		return domain.FetchResult{}, errors.New("Reuters取得コンテキストがnilです")
	}
	if connector == nil {
		return domain.FetchResult{}, errors.New("Reutersコネクタがnilです")
	}
	if connector.client == nil {
		return domain.FetchResult{}, errors.New("Reuters HTTPクライアントがnilです")
	}
	if connector.maxResponseBytes <= 0 {
		return domain.FetchResult{}, errors.New("Reutersレスポンスサイズ上限が不正です")
	}
	if connector.endpointURL == "" {
		return domain.FetchResult{}, errors.New("Reuters固定エンドポイントが未設定です")
	}
	if source.MaxItems < 1 || source.MaxItems > maxSourceItems {
		return domain.FetchResult{}, fmt.Errorf(
			"Reuters取得件数max_itemsは1以上%d以下である必要があります: %d",
			maxSourceItems,
			source.MaxItems,
		)
	}

	currentSettings, err := loadSettings(source.Settings)
	if err != nil {
		return domain.FetchResult{}, err
	}

	summaries, err := connector.fetchSection(
		ctx,
		connector.endpointURL,
		currentSettings,
		source.MaxItems,
	)
	if err != nil {
		return domain.FetchResult{}, fmt.Errorf("Reuters記事一覧の取得に失敗しました: %w", err)
	}
	if len(summaries) > source.MaxItems {
		summaries = summaries[:source.MaxItems]
	}

	result := domain.FetchResult{
		Articles: make([]domain.Article, 0, len(summaries)),
		Warnings: make([]string, 0),
	}
	resultBytes := 0
	for index, summary := range summaries {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("Reuters記事処理が中断されました: %w", err)
		}

		externalID := strings.TrimSpace(summary.ID)
		revisionID := summaryRevisionID(summary)
		if connector.revisionChecker != nil && externalID != "" && revisionID != "" {
			exists, existsErr := connector.revisionChecker.ArticleRevisionExists(
				ctx,
				source.ID,
				externalID,
				revisionID,
			)
			if existsErr != nil {
				result.Warnings = append(
					result.Warnings,
					fmt.Sprintf(
						"Reuters記事リビジョンの存在確認に失敗しました: index=%d id=%q revision_id=%q: %v",
						index,
						externalID,
						revisionID,
						existsErr,
					),
				)
			} else if exists {
				result.Skipped++
				continue
			}
		}

		detail, rawPayload, detailErr := connector.fetchDetail(
			ctx,
			connector.endpointURL,
			currentSettings.Website,
			summary,
		)
		if detailErr != nil {
			if errors.Is(detailErr, context.Canceled) ||
				errors.Is(detailErr, context.DeadlineExceeded) {
				return result, detailErr
			}

			result.Warnings = append(
				result.Warnings,
				fmt.Sprintf(
					"Reuters記事詳細の取得に失敗しました: index=%d id=%q canonical_url=%q: %v",
					index,
					strings.TrimSpace(summary.ID),
					strings.TrimSpace(summary.CanonicalURL),
					detailErr,
				),
			)
			continue
		}

		article, convertErr := convertArticle(source.ID, detail, rawPayload)
		if convertErr != nil {
			result.Warnings = append(
				result.Warnings,
				fmt.Sprintf(
					"Reuters記事詳細の変換に失敗しました: index=%d id=%q canonical_url=%q: %v",
					index,
					strings.TrimSpace(summary.ID),
					strings.TrimSpace(summary.CanonicalURL),
					convertErr,
				),
			)
			continue
		}
		canonicalURL, resolveErr := resolveCanonicalURL(
			connector.endpointURL,
			article.CanonicalURL,
		)
		if resolveErr != nil {
			result.Warnings = append(
				result.Warnings,
				fmt.Sprintf(
					"Reuters記事URLの変換に失敗しました: index=%d id=%q canonical_url=%q: %v",
					index,
					article.ExternalID,
					article.CanonicalURL,
					resolveErr,
				),
			)
			continue
		}
		article.CanonicalURL = canonicalURL

		articleBytes := len(article.Headline) +
			len(article.BodyText) +
			len(article.RawPayload)
		if resultBytes+articleBytes > maxFetchResultBytes {
			result.Warnings = append(
				result.Warnings,
				fmt.Sprintf(
					"Reuters取得結果が総量上限を超えるため残りを省略しました: limit=%d",
					maxFetchResultBytes,
				),
			)
			break
		}
		resultBytes += articleBytes
		result.Articles = append(result.Articles, article)
	}

	return result, nil
}

// ----------------------------------------

// resolveCanonicalURL は、Reutersの相対canonical URLを絶対URLへ変換します。
//
// 機能:
//   - endpoint URLを基準に相対パスを解決する
//   - httpまたはhttpsのホスト付きURLだけを許可する
//   - URL内のユーザー情報を拒否する
//
// 引数:
//   - endpointURL (string): Reuters APIと公開記事の基準URL
//   - canonicalURL (string): 詳細レスポンスに含まれる絶対または相対URL
//
// 返り値:
//   - string: 検証済みの絶対canonical URL
//   - error: 基準URLまたはcanonical URLが不正な場合のエラー。成功時はnil
func resolveCanonicalURL(endpointURL string, canonicalURL string) (string, error) {
	baseURL, err := url.Parse(strings.TrimSpace(endpointURL))
	if err != nil {
		return "", fmt.Errorf("Reuters endpoint_urlを解析できません: %w", err)
	}
	reference, err := url.Parse(strings.TrimSpace(canonicalURL))
	if err != nil {
		return "", fmt.Errorf("Reuters canonical_urlを解析できません: %w", err)
	}
	resolved := baseURL.ResolveReference(reference)
	if (resolved.Scheme != "http" && resolved.Scheme != "https") ||
		resolved.Hostname() == "" {
		return "", fmt.Errorf("Reuters canonical_urlはhttpまたはhttpsの絶対URLが必要です: %q", canonicalURL)
	}
	if resolved.User != nil {
		return "", errors.New("Reuters canonical_urlにユーザー情報は指定できません")
	}
	return resolved.String(), nil
}

// ----------------------------------------

// summaryRevisionID は、Reuters一覧レスポンスから存在確認用リビジョンを取得します。
//
// 機能:
//   - revision_idを優先し、空の場合はrevisionへフォールバックする
//   - 前後空白を除去する
//
// 引数:
//   - summary (articleSummary): Reuters一覧の記事概要
//
// 返り値:
//   - string: 保存済み確認に使うリビジョンID。両方空の場合は空文字
func summaryRevisionID(summary articleSummary) string {
	if revisionID := strings.TrimSpace(summary.RevisionID); revisionID != "" {
		return revisionID
	}
	return strings.TrimSpace(summary.Revision)
}

// ----------------------------------------

// loadSettings は、Source.SettingsからReuters固有設定を読み込みます。
//
// 機能:
//   - section_idだけを保存設定から読み込む
//   - websiteとoffsetを参照実装で確認した固定値へ設定する
//   - section_idを取得対象ホワイトリストで検証する
//
// 引数:
//   - rawSettings (json.RawMessage): Reuters固有設定を格納したJSON
//
// 返り値:
//   - settings: 既定値を補完したReuters固有設定
//   - error: JSON解析または設定値検証に失敗した場合のエラー。成功時はnil
func loadSettings(rawSettings json.RawMessage) (settings, error) {
	currentSettings := settings{
		SectionID: defaultSectionID,
		Website:   WebsiteID,
		Offset:    0,
	}

	trimmedSettings := bytes.TrimSpace(rawSettings)
	if len(trimmedSettings) == 0 || bytes.Equal(trimmedSettings, []byte("null")) {
		return currentSettings, nil
	}

	var configured struct {
		SectionID string `json:"section_id"`
	}
	if err := json.Unmarshal(trimmedSettings, &configured); err != nil {
		return settings{}, fmt.Errorf("Reuters settingsのJSON解析に失敗しました: %w", err)
	}

	sectionID := strings.TrimSpace(configured.SectionID)
	if sectionID == "" {
		sectionID = defaultSectionID
	}
	target, ok := FindTargetBySectionID(sectionID)
	if !ok {
		return settings{}, fmt.Errorf("Reutersの取得対象section_idが未対応です: %q", sectionID)
	}
	currentSettings.SectionID = target.SectionID

	return currentSettings, nil
}

// ----------------------------------------

// fetchSection は、Reutersの記事一覧取得APIから記事概要を取得します。
//
// 機能:
//   - Reuters固有設定と最大件数から一覧用query JSONを作成する
//   - 既定の記事一覧fetch sourceへリクエストする
//   - APIの業務ステータスとresultの存在を検証する
//   - JSONレスポンスから記事概要一覧を取り出す
//
// 引数:
//   - ctx (context.Context): HTTPリクエストへ伝播するコンテキスト
//   - endpointURL (string): Reuters APIのベースURL
//   - currentSettings (settings): section_id、website、offsetの取得設定
//   - maxItems (int): 一覧APIへ要求する最大記事件数
//
// 返り値:
//   - []articleSummary: 記事詳細取得に使用する記事概要一覧
//   - error: URL生成、HTTP通信、JSON解析、API結果検証に失敗した場合のエラー。成功時はnil
func (connector *Connector) fetchSection(
	ctx context.Context,
	endpointURL string,
	currentSettings settings,
	maxItems int,
) ([]articleSummary, error) {
	query := sectionQuery{
		ArcSite:   currentSettings.Website,
		FetchType: "collection",
		Offset:    currentSettings.Offset,
		SectionID: currentSettings.SectionID,
		Size:      maxItems,
		Website:   currentSettings.Website,
	}

	var response sectionResponse
	if _, err := connector.fetchJSON(ctx, endpointURL, sectionFetchSource, query, &response); err != nil {
		return nil, err
	}
	if response.StatusCode != 0 && response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"Reuters記事一覧APIが業務エラーを返しました: statusCode=%d message=%q",
			response.StatusCode,
			strings.TrimSpace(response.Message),
		)
	}
	if response.Result == nil {
		return nil, fmt.Errorf(
			"Reuters記事一覧APIのresultが未設定です: message=%q",
			strings.TrimSpace(response.Message),
		)
	}

	return response.Result.Articles, nil
}

// ----------------------------------------

// fetchDetail は、Reutersの記事詳細取得APIから本文を含む記事詳細を取得します。
//
// 機能:
//   - 記事IDを優先し、存在しない場合はcanonical URLを検索キーにする
//   - 既定の記事詳細fetch sourceへリクエストする
//   - APIの業務ステータスとresultの存在を検証する
//   - 変換前のJSONと記事詳細を返す
//
// 引数:
//   - ctx (context.Context): HTTPリクエストへ伝播するコンテキスト
//   - endpointURL (string): Reuters APIのベースURL
//   - website (string): Reuters APIへ渡すwebsite
//   - summary (articleSummary): 詳細取得対象の記事概要
//
// 返り値:
//   - articleDetail: Reuters APIから取得した記事詳細
//   - json.RawMessage: 保存用の未加工JSONレスポンス
//   - error: 検索キー、通信、JSON解析、API結果検証に失敗した場合のエラー。成功時はnil
func (connector *Connector) fetchDetail(
	ctx context.Context,
	endpointURL string,
	website string,
	summary articleSummary,
) (articleDetail, json.RawMessage, error) {
	query := articleQuery{
		Website: website,
	}

	if articleID := strings.TrimSpace(summary.ID); articleID != "" {
		query.ID = articleID
	} else if canonicalURL := strings.TrimSpace(summary.CanonicalURL); canonicalURL != "" {
		query.WebsiteURL = canonicalURL
	} else {
		return articleDetail{}, nil, errors.New("記事IDとcanonical URLの両方が未設定です")
	}

	var response articleDetailResponse
	rawPayload, err := connector.fetchJSON(
		ctx,
		endpointURL,
		articleFetchSource,
		query,
		&response,
	)
	if err != nil {
		return articleDetail{}, nil, err
	}
	if response.StatusCode != 0 && response.StatusCode != http.StatusOK {
		return articleDetail{}, nil, fmt.Errorf(
			"Reuters記事詳細APIが業務エラーを返しました: statusCode=%d message=%q",
			response.StatusCode,
			strings.TrimSpace(response.Message),
		)
	}
	if response.Result == nil {
		return articleDetail{}, nil, fmt.Errorf(
			"Reuters記事詳細APIのresultが未設定です: message=%q",
			strings.TrimSpace(response.Message),
		)
	}

	return *response.Result, json.RawMessage(rawPayload), nil
}

// ----------------------------------------

// fetchJSON は、Reuters APIへGETリクエストを送りJSONレスポンスを解析します。
//
// 機能:
//   - ペイロードをquery JSONとしてURLへ付与する
//   - コンテキスト、Accept、User-Agentを設定してHTTPリクエストを実行する
//   - レスポンスサイズとHTTPステータスを検証してJSONを解析する
//
// 引数:
//   - ctx (context.Context): HTTPリクエストへ伝播するコンテキスト
//   - endpointURL (string): Reuters APIのベースURL
//   - fetchSource (string): Reuters APIのfetch source名
//   - queryPayload (any): queryパラメータへJSON文字列として格納する値
//   - output (any): JSONレスポンスの解析先ポインタ
//
// 返り値:
//   - []byte: 読み取った未加工レスポンス
//   - error: URL生成、HTTP通信、読み取り、ステータス、JSON解析に失敗した場合のエラー。成功時はnil
func (connector *Connector) fetchJSON(
	ctx context.Context,
	endpointURL string,
	fetchSource string,
	queryPayload any,
	output any,
) ([]byte, error) {
	requestURL, err := buildFetchURL(endpointURL, fetchSource, queryPayload)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("Reuters HTTPリクエストの作成に失敗しました: %w", err)
	}
	request.Header.Set("Accept", acceptHeader)
	request.Header.Set("User-Agent", connector.userAgent)

	response, err := connector.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Reuters APIへのリクエストに失敗しました: %w", err)
	}
	if response == nil {
		return nil, errors.New("Reuters APIからnilレスポンスが返されました")
	}
	if response.Body == nil {
		return nil, errors.New("Reuters APIレスポンスのbodyがnilです")
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		preview, exceeded, previewErr := readBoundedBody(response.Body, maxErrorBodyPreview)
		if previewErr != nil {
			return nil, fmt.Errorf("Reuters APIエラーレスポンスの読み取りに失敗しました: %w", previewErr)
		}
		truncated := ""
		if exceeded {
			truncated = "（プレビュー上限で省略）"
		}
		return nil, fmt.Errorf(
			"Reuters APIが異常終了しました: status=%d url=%q body=%q%s",
			response.StatusCode,
			requestURL,
			string(preview),
			truncated,
		)
	}

	body, exceeded, err := readBoundedBody(response.Body, connector.maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("Reuters APIレスポンスの読み取りに失敗しました: %w", err)
	}

	if exceeded {
		return nil, fmt.Errorf(
			"Reuters APIレスポンスがサイズ上限を超えました: max_response_bytes=%d",
			connector.maxResponseBytes,
		)
	}

	if err := json.Unmarshal(body, output); err != nil {
		return nil, fmt.Errorf("Reuters APIレスポンスのJSON解析に失敗しました: %w", err)
	}

	return body, nil
}

// ----------------------------------------

// buildFetchURL は、Reuters APIのfetch URLとqueryパラメータを組み立てます。
//
// 機能:
//   - ベースURLへReutersコンテンツ取得APIのパスを追加する
//   - queryPayloadをJSONへ変換してqueryパラメータへ設定する
//   - httpまたはhttpsの絶対URLだけを許可し、ユーザー情報を拒否する
//   - ベースURLの既存パスを保持し、既存クエリとフラグメントを除去する
//
// 引数:
//   - endpointURL (string): Reuters APIのベースURL
//   - fetchSource (string): Reuters APIのfetch source名
//   - queryPayload (any): queryパラメータへ格納する値
//
// 返り値:
//   - string: エンコード済みのReuters APIリクエストURL
//   - error: ベースURLまたはquery JSONが不正な場合のエラー。成功時はnil
func buildFetchURL(endpointURL string, fetchSource string, queryPayload any) (string, error) {
	baseURL, err := url.Parse(strings.TrimSpace(endpointURL))
	if err != nil {
		return "", fmt.Errorf("Reuters endpoint_urlの解析に失敗しました: %w", err)
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return "", fmt.Errorf("Reuters endpoint_urlにはschemeとhostが必要です: %q", endpointURL)
	}
	if !strings.EqualFold(baseURL.Scheme, "http") &&
		!strings.EqualFold(baseURL.Scheme, "https") {
		return "", fmt.Errorf(
			"Reuters endpoint_urlのschemeはhttpまたはhttpsである必要があります: %q",
			baseURL.Scheme,
		)
	}
	if baseURL.User != nil {
		return "", errors.New("Reuters endpoint_urlにユーザー情報は指定できません")
	}

	fetchSource = strings.TrimSpace(fetchSource)
	if fetchSource == "" {
		return "", errors.New("Reuters fetch source名が未設定です")
	}

	queryJSON, err := json.Marshal(queryPayload)
	if err != nil {
		return "", fmt.Errorf("Reuters queryのJSON変換に失敗しました: %w", err)
	}

	baseURL.Path = strings.TrimRight(baseURL.Path, "/") +
		"/pf/api/v3/content/fetch/" +
		url.PathEscape(fetchSource)
	baseURL.RawPath = ""
	baseURL.RawQuery = ""
	baseURL.Fragment = ""

	values := url.Values{}
	values.Set("query", string(queryJSON))
	baseURL.RawQuery = values.Encode()

	return baseURL.String(), nil
}

// ----------------------------------------

// readBoundedBody は、指定したサイズ上限までHTTPレスポンス本文を読み取ります。
//
// 機能:
//   - 上限より1バイト多く読み、レスポンス超過を検出する
//   - 上限を超えた場合も呼び出し元へ返す本文を上限以内に切り詰める
//   - 読み取り時のエラーを呼び出し元へ返す
//
// 引数:
//   - reader (io.Reader): 読み取り対象のHTTPレスポンス本文
//   - maxBytes (int64): 呼び出し元へ返す最大バイト数
//
// 返り値:
//   - []byte: 最大maxBytesまでのレスポンス本文
//   - bool: 本文がmaxBytesを超えた場合はtrue
//   - error: 本文の読み取りに失敗した場合のエラー。成功時はnil
func readBoundedBody(reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	if reader == nil {
		return nil, false, errors.New("レスポンス本文のreaderがnilです")
	}
	if maxBytes <= 0 {
		return nil, false, fmt.Errorf("レスポンスサイズ上限は1以上である必要があります: %d", maxBytes)
	}

	readLimit := maxBytes + 1
	if readLimit <= maxBytes {
		readLimit = maxBytes
	}

	body, err := io.ReadAll(io.LimitReader(reader, readLimit))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) <= maxBytes {
		return body, false, nil
	}

	return body[:maxBytes], true, nil
}

// ----------------------------------------

// convertArticle は、Reutersの記事詳細を共通の記事ドメインへ変換します。
//
// 機能:
//   - published_timeを必須のRFC3339Nano日時として解析する
//   - updated_timeが存在する場合だけRFC3339Nano日時として解析する
//   - 見出し、識別子、URL、本文、未加工JSONを共通形式へ格納する
//   - 保存に必要な識別子、URL、見出し、本文が空でないことを検証する
//
// 引数:
//   - sourceID (int64): 記事の取得元ID
//   - detail (articleDetail): Reuters APIから取得した記事詳細
//   - rawPayload (json.RawMessage): Reuters APIの未加工記事詳細レスポンス
//
// 返り値:
//   - domain.Article: 共通形式へ変換した記事
//   - error: 必須項目の欠落または日時解析に失敗した場合のエラー。成功時はnil
func convertArticle(
	sourceID int64,
	detail articleDetail,
	rawPayload json.RawMessage,
) (domain.Article, error) {
	publishedAt, err := parseRequiredTime("published_time", detail.PublishedTime)
	if err != nil {
		return domain.Article{}, err
	}

	updatedAt, err := parseOptionalTime("updated_time", detail.UpdatedTime)
	if err != nil {
		return domain.Article{}, err
	}

	article := domain.Article{
		SourceID:     sourceID,
		Provider:     reutersProvider,
		ExternalID:   strings.TrimSpace(detail.ID),
		RevisionID:   strings.TrimSpace(detail.RevisionID),
		CanonicalURL: strings.TrimSpace(detail.CanonicalURL),
		PublishedAt:  publishedAt,
		UpdatedAt:    updatedAt,
		Headline:     strings.TrimSpace(detail.Title),
		BodyText:     buildBodyText(detail),
		RawPayload:   compactRawPayload(rawPayload),
	}

	if article.ExternalID == "" {
		return domain.Article{}, errors.New("external_idに対応するresult.idが未設定です")
	}
	if article.RevisionID == "" {
		return domain.Article{}, errors.New("revision_idに対応するresult.revision_idが未設定です")
	}
	if article.CanonicalURL == "" {
		return domain.Article{}, errors.New("canonical_urlに対応するresult.canonical_urlが未設定です")
	}
	if article.Headline == "" {
		return domain.Article{}, errors.New("headlineに対応するresult.titleが未設定です")
	}
	if article.BodyText == "" {
		return domain.Article{}, errors.New(
			"body_textに対応するresult.datelineまたはparagraphが未設定です",
		)
	}
	if len(article.BodyText) > maxArticleBodyBytes {
		return domain.Article{}, fmt.Errorf(
			"body_textがサイズ上限を超えています: bytes=%d limit=%d",
			len(article.BodyText),
			maxArticleBodyBytes,
		)
	}

	return article, nil
}

// ----------------------------------------

// compactRawPayload は、大きな未加工レスポンスをハッシュ付きメタデータへ置き換えます。
//
// 機能:
//   - 上限以内のJSONは内容をコピーして保持する
//   - 上限超過時は元サイズとSHA-256だけを有効なJSONとして保持する
//   - 記事一覧をメモリ上へ蓄積する際の過大消費を防ぐ
//
// 引数:
//   - rawPayload (json.RawMessage): Reuters APIの未加工詳細レスポンス
//
// 返り値:
//   - json.RawMessage: 原文コピーまたは切り詰めメタデータ
func compactRawPayload(rawPayload json.RawMessage) json.RawMessage {
	if len(rawPayload) <= maxRawPayloadBytes {
		return append(json.RawMessage(nil), rawPayload...)
	}

	digest := sha256.Sum256(rawPayload)
	metadata, err := json.Marshal(map[string]any{
		"truncated":      true,
		"original_bytes": len(rawPayload),
		"sha256":         hex.EncodeToString(digest[:]),
	})
	if err != nil {
		return json.RawMessage(`{"truncated":true}`)
	}
	return metadata
}

// ----------------------------------------

// buildBodyText は、Reutersの記事詳細から保存用本文を組み立てます。
//
// 機能:
//   - 空でないdatelineを本文の先頭へ追加する
//   - content_elementsのparagraph要素だけを追加する
//   - 各要素の前後空白を除去し、改行で連結する
//
// 引数:
//   - detail (articleDetail): 本文構成要素を持つReuters記事詳細
//
// 返り値:
//   - string: datelineとparagraphを改行で連結した本文
func buildBodyText(detail articleDetail) string {
	parts := make([]string, 0, len(detail.Dateline)+len(detail.ContentElements))
	for _, dateline := range detail.Dateline {
		if trimmed := strings.TrimSpace(dateline); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	for _, element := range detail.ContentElements {
		if element.Type != "paragraph" {
			continue
		}
		if content := strings.TrimSpace(element.Content); content != "" {
			parts = append(parts, content)
		}
	}

	return strings.Join(parts, "\n")
}

// ----------------------------------------

// parseRequiredTime は、必須日時をRFC3339Nano形式で解析します。
//
// 機能:
//   - 日時文字列の前後空白を除去する
//   - 空文字を必須項目エラーとして扱う
//   - RFC3339Nano形式の日時をtime.Timeポインタへ変換する
//
// 引数:
//   - fieldName (string): エラーへ含める項目名
//   - value (string): 解析対象の日時文字列
//
// 返り値:
//   - *time.Time: 解析済みの日時
//   - error: 未設定またはRFC3339Nano解析に失敗した場合のエラー。成功時はnil
func parseRequiredTime(fieldName string, value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("%sが未設定です", fieldName)
	}

	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return nil, fmt.Errorf("%sの日時解析に失敗しました: %w", fieldName, err)
	}

	return &parsed, nil
}

// ----------------------------------------

// parseOptionalTime は、任意日時をRFC3339Nano形式で解析します。
//
// 機能:
//   - 空文字の場合は未設定を表すnilを返す
//   - 値が存在する場合はRFC3339Nano形式で解析する
//   - 日時文字列の前後空白を除去する
//
// 引数:
//   - fieldName (string): エラーへ含める項目名
//   - value (string): 解析対象の任意日時文字列
//
// 返り値:
//   - *time.Time: 解析済み日時。未設定の場合はnil
//   - error: RFC3339Nano解析に失敗した場合のエラー。成功時はnil
func parseOptionalTime(fieldName string, value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return nil, fmt.Errorf("%sの日時解析に失敗しました: %w", fieldName, err)
	}

	return &parsed, nil
}
