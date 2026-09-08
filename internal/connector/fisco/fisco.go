// Package fisco は、Fisco の固定ニュース API から記事を取得します。
package fisco

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/domain"
)

const (
	// EndpointURL は、Fisco の固定記事一覧 API URL です。
	EndpointURL = "https://web.fisco.jp/FiscoWeb/news"

	articlePageURL         = "https://web.fisco.jp/platform/market-news/"
	defaultUserAgent       = "NewsStore/1.0"
	defaultMaxResponseSize = int64(4 * 1024 * 1024)
	maxSourceItems         = 50
	maxArticleBodyBytes    = 2 * 1024 * 1024
	maxRawPayloadBytes     = 512 * 1024
	maxFetchResultBytes    = 32 * 1024 * 1024
	maxErrorBodyPreview    = int64(4 * 1024)
)

var japanTime = time.FixedZone("JST", 9*60*60)

// Doer は、Fisco API の HTTP リクエストを実行する最小インターフェースです。
type Doer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Connector は、Fisco の記事一覧と記事詳細を取得するコネクタです。
type Connector struct {
	client           Doer
	userAgent        string
	maxResponseBytes int64
}

// articleSummary は、一覧に含まれる記事の識別子、見出し、公開日時です。
type articleSummary struct {
	ID       string `json:"newsId"`
	Title    string `json:"newsTitle"`
	DateTime string `json:"newsDateTime"`
}

// listResponse は、空の一覧と data 欠落を区別する一覧レスポンスです。
type listResponse struct {
	Data *[]articleSummary `json:"data"`
}

// detailResponse は、詳細レスポンス内の news から取得する本文です。
type detailResponse struct {
	News *struct {
		ID     string `json:"newsId"`
		Report string `json:"newsReport"`
	} `json:"news"`
}

// ----------------------------------------

// New は、固定 Fisco API を使用するコネクタを生成します。
//
// 機能:
//   - 未指定の HTTP クライアント、User-Agent、レスポンスサイズ上限を補完する
//
// 引数:
//   - client (Doer): HTTP クライアント。nil の場合は http.DefaultClient
//   - userAgent (string): 送信する User-Agent。空の場合は既定値
//   - maxResponseBytes (int64): レスポンスの最大バイト数。0 以下の場合は既定値
//
// 返り値:
//   - *Connector: 初期化済みの Fisco コネクタ
func New(client Doer, userAgent string, maxResponseBytes int64) *Connector {
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
	return &Connector{client: client, userAgent: userAgent, maxResponseBytes: maxResponseBytes}
}

// ----------------------------------------

// Fetch は、固定の一覧 API と各記事の詳細 API から Fisco 記事を取得します。
//
// 機能:
//   - isRemarkable=false と最大取得件数を limit に指定して一覧を取得する
//   - Source.EndpointURL と Source.Settings に依存せず、取得先を固定する
//   - 一覧の見出しと日本時間の日時、詳細の news.newsReport を保存する
//   - 個別記事の失敗は警告として継続し、キャンセル時は処理を中断する
//
// 引数:
//   - ctx (context.Context): HTTP 通信へ伝播するキャンセルと期限
//   - source (domain.Source): 取得元 ID と最大取得件数
//
// 返り値:
//   - domain.FetchResult: 取得できた記事と個別警告
//   - error: 入力不備、一覧取得失敗、中断時のエラー。成功時は nil
func (connector *Connector) Fetch(ctx context.Context, source domain.Source) (domain.FetchResult, error) {
	if ctx == nil {
		return domain.FetchResult{}, errors.New("Fisco 取得コンテキストが nil です")
	}
	if connector == nil || connector.client == nil {
		return domain.FetchResult{}, errors.New("Fisco コネクタまたは HTTP クライアントが nil です")
	}
	if connector.maxResponseBytes <= 0 {
		return domain.FetchResult{}, errors.New("Fisco レスポンスサイズ上限が不正です")
	}
	if source.MaxItems < 1 || source.MaxItems > maxSourceItems {
		return domain.FetchResult{}, fmt.Errorf("Fisco max_items は 1 以上 %d 以下が必要です: %d", maxSourceItems, source.MaxItems)
	}

	// ----------------------------------------

	query := url.Values{"isRemarkable": {"false"}, "limit": {strconv.Itoa(source.MaxItems)}}
	var listing listResponse
	if _, err := connector.fetchJSON(ctx, EndpointURL+"?"+query.Encode(), &listing); err != nil {
		return domain.FetchResult{}, fmt.Errorf("Fisco 記事一覧の取得に失敗しました: %w", err)
	}
	if listing.Data == nil {
		return domain.FetchResult{}, errors.New("Fisco 記事一覧の data が未設定です")
	}
	summaries := *listing.Data
	if len(summaries) > source.MaxItems {
		summaries = summaries[:source.MaxItems]
	}

	// ----------------------------------------

	result := domain.FetchResult{
		Articles: make([]domain.Article, 0, len(summaries)),
		Warnings: make([]string, 0),
	}
	resultBytes := 0
	for index, summary := range summaries {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("Fisco 記事処理が中断されました: %w", err)
		}
		article, err := connector.fetchArticle(ctx, source.ID, summary)
		if err != nil {
			if ctx.Err() != nil {
				return result, fmt.Errorf("Fisco 記事処理が中断されました: %w", ctx.Err())
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return result, err
			}
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Fisco 記事の取得に失敗しました: index=%d id=%q: %v", index, summary.ID, err))
			continue
		}
		articleBytes := len(article.Headline) + len(article.BodyText) + len(article.RawPayload)
		if resultBytes+articleBytes > maxFetchResultBytes {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Fisco 取得結果が総量上限を超えるため残りを省略しました: limit=%d", maxFetchResultBytes))
			break
		}
		resultBytes += articleBytes
		result.Articles = append(result.Articles, article)
	}
	return result, nil
}

// ----------------------------------------

// fetchArticle は、一覧の必須項目を検証して詳細本文を共通記事へ変換します。
//
// 機能:
//   - newsId を安全な単一パスとして検証し、公式記事 URL を生成する
//   - 日時を日本時間で解析し、銘柄コードを含む本文をそのまま保持する
//   - 見出し、日時、本文からリビジョンを作り、本文更新の取りこぼしを防ぐ
//
// 引数:
//   - ctx (context.Context): HTTP 通信へ伝播するキャンセルと期限
//   - sourceID (int64): 取得元 ID
//   - summary (articleSummary): 一覧から取得した記事概要
//
// 返り値:
//   - domain.Article: 詳細本文を含む共通記事
//   - error: 必須項目、日時、詳細通信、サイズの検証に失敗した場合のエラー
func (connector *Connector) fetchArticle(ctx context.Context, sourceID int64, summary articleSummary) (domain.Article, error) {
	id := strings.TrimSpace(summary.ID)
	if err := validateArticleID(id); err != nil {
		return domain.Article{}, err
	}
	headline := strings.TrimSpace(summary.Title)
	if headline == "" {
		return domain.Article{}, errors.New("headline に対応する newsTitle が未設定です")
	}
	publishedAt, err := time.ParseInLocation("20060102150405", strings.TrimSpace(summary.DateTime), japanTime)
	if err != nil {
		return domain.Article{}, fmt.Errorf("newsDateTime の日時解析に失敗しました: %w", err)
	}

	// ----------------------------------------

	var detail detailResponse
	rawPayload, err := connector.fetchJSON(ctx, EndpointURL+"/"+url.PathEscape(id), &detail)
	if err != nil {
		return domain.Article{}, err
	}
	if detail.News == nil {
		return domain.Article{}, errors.New("Fisco 記事詳細の news が未設定です")
	}
	if detailID := strings.TrimSpace(detail.News.ID); detailID != "" && detailID != id {
		return domain.Article{}, fmt.Errorf("Fisco 記事詳細の newsId が一覧と一致しません: %q", detailID)
	}
	bodyText := strings.TrimSpace(detail.News.Report)
	if bodyText == "" {
		return domain.Article{}, errors.New("body_text に対応する news.newsReport が未設定です")
	}
	if len(bodyText) > maxArticleBodyBytes {
		return domain.Article{}, fmt.Errorf("body_text がサイズ上限を超えています: bytes=%d limit=%d", len(bodyText), maxArticleBodyBytes)
	}

	// ----------------------------------------

	revisionPayload, err := json.Marshal([]string{headline, publishedAt.Format(time.RFC3339), bodyText})
	if err != nil {
		return domain.Article{}, fmt.Errorf("Fisco リビジョンの JSON 変換に失敗しました: %w", err)
	}
	digest := sha256.Sum256(revisionPayload)
	return domain.Article{
		SourceID:     sourceID,
		Provider:     "fisco",
		ExternalID:   id,
		RevisionID:   hex.EncodeToString(digest[:]),
		CanonicalURL: articlePageURL + url.PathEscape(id),
		PublishedAt:  &publishedAt,
		Headline:     headline,
		BodyText:     bodyText,
		RawPayload:   compactRawPayload(rawPayload),
	}, nil
}

// ----------------------------------------

// validateArticleID は、詳細取得と記事 URL に使う newsId を検証します。
//
// 機能:
//   - 空値とパスやクエリの構造を変える文字を拒否する
//
// 引数:
//   - id (string): 前後空白を除去した記事 ID
//
// 返り値:
//   - error: ID が空または英数字、ハイフン、下線以外を含む場合のエラー
func validateArticleID(id string) error {
	if id == "" {
		return errors.New("external_id に対応する newsId が未設定です")
	}
	for _, character := range id {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') || character == '-' || character == '_' {
			continue
		}
		return fmt.Errorf("newsId に使用できない文字が含まれています: %q", id)
	}
	return nil
}

// ----------------------------------------

// fetchJSON は、サイズを制限して Fisco API の JSON を取得します。
//
// 機能:
//   - コンテキストと共通ヘッダーを設定して GET リクエストを実行する
//   - HTTP ステータス、サイズ上限、JSON 形式を検証する
//
// 引数:
//   - ctx (context.Context): HTTP 通信へ伝播するキャンセルと期限
//   - requestURL (string): 固定 API を基準に生成した取得先 URL
//   - output (any): JSON の解析先ポインタ
//
// 返り値:
//   - []byte: 未加工のレスポンス本文
//   - error: 通信、サイズ、JSON 解析のエラー。成功時は nil
func (connector *Connector) fetchJSON(ctx context.Context, requestURL string, output any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("Fisco HTTP リクエストの作成に失敗しました: %w", err)
	}
	request.Header.Set("Accept", "application/json,text/plain,*/*")
	request.Header.Set("User-Agent", connector.userAgent)
	response, err := connector.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Fisco API へのリクエストに失敗しました: %w", err)
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("Fisco API レスポンスまたは本文が nil です")
	}
	defer response.Body.Close()

	// ----------------------------------------

	limit := connector.maxResponseBytes
	if response.StatusCode != http.StatusOK && limit > maxErrorBodyPreview {
		limit = maxErrorBodyPreview
	}
	readLimit := limit + 1
	if readLimit <= limit {
		readLimit = limit
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, readLimit))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("Fisco API レスポンスの読み取りに失敗しました: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		if int64(len(body)) > limit {
			body = body[:limit]
		}
		return nil, fmt.Errorf("Fisco API が異常終了しました: status=%d body=%q", response.StatusCode, string(body))
	}
	if int64(len(body)) > connector.maxResponseBytes {
		return nil, fmt.Errorf("Fisco API レスポンスがサイズ上限を超えました: max_response_bytes=%d", connector.maxResponseBytes)
	}
	if err := json.Unmarshal(body, output); err != nil {
		return nil, fmt.Errorf("Fisco API レスポンスの JSON 解析に失敗しました: %w", err)
	}
	return body, nil
}

// ----------------------------------------

// compactRawPayload は、大きな詳細レスポンスをハッシュ付きメタデータへ変換します。
//
// 機能:
//   - 上限以内の原文はコピーし、超過時は元サイズと SHA-256 を保存する
//
// 引数:
//   - rawPayload ([]byte): Fisco の未加工詳細レスポンス
//
// 返り値:
//   - json.RawMessage: 原文またはサイズ超過のメタデータ
func compactRawPayload(rawPayload []byte) json.RawMessage {
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
