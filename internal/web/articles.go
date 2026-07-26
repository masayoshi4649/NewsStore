package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/articlesearch"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

const (
	defaultWebArticleLimit      = 20
	maxWebArticleLimit          = articlesearch.MaxLimit
	defaultWebArticleBodyLimit  = 8000
	maxWebArticleBodyLimit      = 16000
	maxWebArticleBodyOffset     = 10000000
	webArticleRepositoryTimeout = 60 * time.Second
)

// articleAPIResponse は、記事APIの一覧結果とページ情報です。
type articleAPIResponse struct {
	Articles   []articleAPISummary `json:"articles"`
	HasMore    bool                `json:"has_more"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// articleAPISummary は、記事APIが返す本文抜粋付きの記事概要です。
type articleAPISummary struct {
	ID           int64  `json:"id"`
	SourceID     int64  `json:"source_id"`
	SourceName   string `json:"source_name"`
	Provider     string `json:"provider"`
	ExternalID   string `json:"external_id"`
	RevisionID   string `json:"revision_id"`
	CanonicalURL string `json:"canonical_url"`
	PublishedAt  string `json:"published_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	Headline     string `json:"headline"`
	BodyExcerpt  string `json:"body_excerpt"`
	FetchedAt    string `json:"fetched_at"`
	EffectiveAt  string `json:"effective_at"`
}

// articleAPIDetailResponse は、記事APIの詳細結果です。
type articleAPIDetailResponse struct {
	Article articleAPIDetail `json:"article"`
}

// articleAPIDetail は、記事APIが返す本文断片付きの記事詳細です。
type articleAPIDetail struct {
	ID             int64  `json:"id"`
	SourceID       int64  `json:"source_id"`
	SourceName     string `json:"source_name"`
	Provider       string `json:"provider"`
	ExternalID     string `json:"external_id"`
	RevisionID     string `json:"revision_id"`
	CanonicalURL   string `json:"canonical_url"`
	PublishedAt    string `json:"published_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	Headline       string `json:"headline"`
	BodyText       string `json:"body_text"`
	BodyOffset     int    `json:"body_offset"`
	HasMore        bool   `json:"has_more"`
	NextBodyOffset int    `json:"next_body_offset,omitempty"`
	FetchedAt      string `json:"fetched_at"`
	EffectiveAt    string `json:"effective_at"`
}

// sourceAPIResponse は、記事検索で選択できる取得対象一覧です。
type sourceAPIResponse struct {
	Sources []sourceAPIItem `json:"sources"`
}

// sourceAPIItem は、取得対象のIDと画面表示用情報です。
type sourceAPIItem struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}

// articleListPage は、API駆動の記事一覧テンプレートへ渡す共通ページ情報です。
type articleListPage struct {
	Base                basePage
	DefaultArticleLimit int
}

// articleDetailPage は、API駆動の記事詳細テンプレートへ渡す共通ページ情報です。
type articleDetailPage struct {
	Base basePage
}

/*
listArticlesAPI は、保存済み記事をMCPと同じ条件と順序で検索します。

機能:
  - 取得対象、日時範囲、最大5語のAND検索条件を検証する
  - 日時を両方指定した場合は入力順にかかわらず時系列順へ正規化する
  - 条件未指定時は設定された既定件数で最新記事を検索する
  - 実効日時と記事IDによるカーソルページング結果をJSONで返す

引数:
  - c *gin.Context: HTTPリクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) listArticlesAPI(c *gin.Context) {
	sourceID, err := parseArticleAPISourceID(c.Query("source_id"))
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	keywords, err := parseArticleAPIKeywords(c)
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	dateFrom, err := articlesearch.ParseTime("date_from", c.Query("date_from"))
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	dateTo, err := articlesearch.ParseTime("date_to", c.Query("date_to"))
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	dateFrom, dateTo = articlesearch.NormalizeTimeRange(dateFrom, dateTo)
	limit, err := parseArticleAPILimit(c.Query("limit"), s.defaultArticleLimit)
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	beforeTime, beforeID, err := articlesearch.DecodeCursor(c.Query("cursor"))
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}

	repositoryContext, cancel := context.WithTimeout(
		c.Request.Context(),
		webArticleRepositoryTimeout,
	)
	defer cancel()
	page, err := s.store.SearchArticlesForMCP(
		repositoryContext,
		postgres.MCPArticleSearchQuery{
			SourceID:      sourceID,
			Keywords:      keywords,
			PublishedFrom: dateFrom,
			PublishedTo:   dateTo,
			Limit:         limit,
			BeforeTime:    beforeTime,
			BeforeID:      beforeID,
		},
	)
	if err != nil {
		s.logger.Error(
			"記事APIの一覧取得に失敗しました",
			"source_id",
			sourceID,
			"error",
			err,
		)
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(repositoryContext.Err(), context.DeadlineExceeded) {
			respondArticleAPIError(
				c,
				http.StatusGatewayTimeout,
				"記事検索が制限時間を超えました",
			)
			return
		}
		respondArticleAPIError(c, http.StatusInternalServerError, "記事を検索できません")
		return
	}

	response := articleAPIResponse{
		Articles: make([]articleAPISummary, 0, len(page.Items)),
		HasMore:  page.HasMore,
	}
	for _, article := range page.Items {
		response.Articles = append(response.Articles, toArticleAPISummary(article))
	}
	if page.HasMore && len(page.Items) > 0 {
		lastArticle := page.Items[len(page.Items)-1]
		response.NextCursor = articlesearch.EncodeCursor(
			lastArticle.EffectiveAt,
			lastArticle.ID,
		)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, response)
}

// ----------------------------------------

/*
showArticleAPI は、指定された保存済み記事の本文断片を返します。

機能:
  - 正の記事ID、本文位置、本文上限を検証する
  - MCPと同じRepositoryで必要な本文範囲だけを読み込む
  - 続きがある場合は次の本文位置をJSONで返す
  - 記事が存在しない場合は内部情報を含まない404を返す

引数:
  - c *gin.Context: HTTPリクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) showArticleAPI(c *gin.Context) {
	id, err := parsePathID(c, "id")
	if err != nil {
		respondArticleAPIError(c, http.StatusNotFound, "記事が見つかりません")
		return
	}
	bodyOffset, err := parseArticleAPIBodyOffset(c.Query("body_offset"))
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}
	bodyLimit, err := parseArticleAPIBodyLimit(c.Query("body_limit"))
	if err != nil {
		respondArticleAPIError(c, http.StatusBadRequest, err.Error())
		return
	}

	repositoryContext, cancel := context.WithTimeout(
		c.Request.Context(),
		webArticleRepositoryTimeout,
	)
	defer cancel()
	chunk, err := s.store.GetArticleChunkForMCP(
		repositoryContext,
		id,
		bodyOffset,
		bodyLimit,
	)
	if errors.Is(err, postgres.ErrNotFound) {
		respondArticleAPIError(c, http.StatusNotFound, "記事が見つかりません")
		return
	}
	if err != nil {
		s.logger.Error("記事APIの詳細取得に失敗しました", "article_id", id, "error", err)
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(repositoryContext.Err(), context.DeadlineExceeded) {
			respondArticleAPIError(
				c,
				http.StatusGatewayTimeout,
				"記事参照が制限時間を超えました",
			)
			return
		}
		respondArticleAPIError(c, http.StatusInternalServerError, "記事を参照できません")
		return
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, articleAPIDetailResponse{
		Article: toArticleAPIDetail(chunk),
	})
}

// ----------------------------------------

/*
listSourcesAPI は、記事検索で利用できる取得対象設定を返します。

機能:
  - 論理削除されていない取得対象のID、設定名、種別、有効状態を読み込む
  - 画面の検索条件選択肢として利用できるJSONを返す

引数:
  - c *gin.Context: HTTPリクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) listSourcesAPI(c *gin.Context) {
	repositoryContext, cancel := context.WithTimeout(
		c.Request.Context(),
		webArticleRepositoryTimeout,
	)
	defer cancel()
	sources, err := s.store.ListSourcesForMCP(repositoryContext)
	if err != nil {
		s.logger.Error("記事APIの取得対象一覧に失敗しました", "error", err)
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(repositoryContext.Err(), context.DeadlineExceeded) {
			respondArticleAPIError(
				c,
				http.StatusGatewayTimeout,
				"取得対象一覧が制限時間を超えました",
			)
			return
		}
		respondArticleAPIError(c, http.StatusInternalServerError, "取得対象を一覧できません")
		return
	}

	response := sourceAPIResponse{
		Sources: make([]sourceAPIItem, 0, len(sources)),
	}
	for _, source := range sources {
		response.Sources = append(response.Sources, sourceAPIItem{
			ID:      source.ID,
			Name:    source.Name,
			Kind:    source.Kind,
			Enabled: source.Enabled,
		})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, response)
}

// ----------------------------------------

/*
parseArticleAPISourceID は、任意指定の取得対象IDを解析します。

機能:
  - 空文字を取得対象指定なしの0へ変換する
  - 指定時は1以上の64ビット整数だけを受け付ける

引数:
  - value string: source_idクエリの値

返り値:
  - int64: 取得対象ID、または指定なしの0
  - error: 指定値が正整数でない場合のエラー
*/
func parseArticleAPISourceID(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	sourceID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sourceID < 1 {
		return 0, errors.New("source_id は1以上の整数で指定してください")
	}
	return sourceID, nil
}

// ----------------------------------------

/*
parseArticleAPIKeywords は、記事APIの検索語をAND検索用に正規化します。

機能:
  - query、keywords、keywords[]の指定を1つの検索語一覧へ結合する
  - 空文字と大文字小文字を区別しない重複語を除外する
  - 最大5語、1語あたり最大200文字へ制限する

引数:
  - c *gin.Context: 検索クエリを保持するGinコンテキスト

返り値:
  - []string: 正規化済みのAND検索語
  - error: 語数または文字数が上限を超えた場合のエラー
*/
func parseArticleAPIKeywords(c *gin.Context) ([]string, error) {
	keywords := make([]string, 0, len(c.QueryArray("keywords")))
	keywords = append(keywords, c.QueryArray("keywords")...)
	keywords = append(keywords, c.QueryArray("keywords[]")...)
	return articlesearch.NormalizeKeywords(c.Query("query"), keywords)
}

// ----------------------------------------

/*
parseArticleAPILimit は、記事APIの最大取得件数を解析します。

機能:
  - 空文字を設定由来の既定件数へ変換する
  - 指定時は1以上100以下の整数だけを受け付ける

引数:
  - value string: limitクエリの値
  - defaultLimit int: 条件未指定時に使う設定由来の件数

返り値:
  - int: 検索で使用する最大取得件数
  - error: 指定値が範囲内の整数でない場合のエラー
*/
func parseArticleAPILimit(value string, defaultLimit int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return articlesearch.NormalizeLimit(0, defaultLimit)
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit == 0 {
		return 0, fmt.Errorf(
			"limit は1以上%d以下の整数で指定してください",
			maxWebArticleLimit,
		)
	}
	normalized, err := articlesearch.NormalizeLimit(limit, defaultLimit)
	if err != nil {
		return 0, fmt.Errorf(
			"limit は1以上%d以下の整数で指定してください",
			maxWebArticleLimit,
		)
	}
	return normalized, nil
}

// ----------------------------------------

/*
parseArticleAPIBodyOffset は、記事本文の取得開始位置を解析します。

機能:
  - 空文字を本文先頭の0へ変換する
  - 指定時は0以上10000000以下の整数だけを受け付ける

引数:
  - value string: body_offsetクエリの値

返り値:
  - int: 本文先頭から読み飛ばすUnicode文字数
  - error: 指定値が範囲内の整数でない場合のエラー
*/
func parseArticleAPIBodyOffset(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	bodyOffset, err := strconv.Atoi(value)
	if err != nil || bodyOffset < 0 || bodyOffset > maxWebArticleBodyOffset {
		return 0, fmt.Errorf(
			"body_offset は0以上%d以下の整数で指定してください",
			maxWebArticleBodyOffset,
		)
	}
	return bodyOffset, nil
}

// ----------------------------------------

/*
parseArticleAPIBodyLimit は、記事本文の最大取得文字数を解析します。

機能:
  - 空文字を既定の8000文字へ変換する
  - 指定時は1以上16000以下の整数だけを受け付ける

引数:
  - value string: body_limitクエリの値

返り値:
  - int: 1回に取得する最大Unicode文字数
  - error: 指定値が範囲内の整数でない場合のエラー
*/
func parseArticleAPIBodyLimit(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultWebArticleBodyLimit, nil
	}
	bodyLimit, err := strconv.Atoi(value)
	if err != nil || bodyLimit < 1 || bodyLimit > maxWebArticleBodyLimit {
		return 0, fmt.Errorf(
			"body_limit は1以上%d以下の整数で指定してください",
			maxWebArticleBodyLimit,
		)
	}
	return bodyLimit, nil
}

// ----------------------------------------

/*
toArticleAPISummary は、Repositoryの記事概要をJSON返却形式へ変換します。

機能:
  - raw_payloadを含めず、本文抜粋と実効日時を設定する
  - 日時をUTCのRFC3339 Nano形式へ統一する

引数:
  - article postgres.MCPStoredArticle: Repositoryから取得した記事概要

返り値:
  - articleAPISummary: 記事APIが返す記事概要
*/
func toArticleAPISummary(article postgres.MCPStoredArticle) articleAPISummary {
	return articleAPISummary{
		ID:           article.ID,
		SourceID:     article.SourceID,
		SourceName:   article.SourceName,
		Provider:     article.Provider,
		ExternalID:   article.ExternalID,
		RevisionID:   article.RevisionID,
		CanonicalURL: article.CanonicalURL,
		PublishedAt:  formatArticleAPIOptionalTime(article.PublishedAt),
		UpdatedAt:    formatArticleAPIOptionalTime(article.UpdatedAt),
		Headline:     article.Headline,
		BodyExcerpt:  article.BodyText,
		FetchedAt:    formatArticleAPIRequiredTime(article.FetchedAt),
		EffectiveAt:  formatArticleAPIRequiredTime(article.EffectiveAt),
	}
}

// ----------------------------------------

/*
toArticleAPIDetail は、Repositoryの記事本文断片をJSON返却形式へ変換します。

機能:
  - 本文断片、本文位置、続きの有無と画面表示用メタデータを設定する
  - 公開日時がない場合は取得日時を実効日時として使用する

引数:
  - chunk postgres.MCPArticleChunk: Repositoryから取得した記事本文断片

返り値:
  - articleAPIDetail: 記事APIが返す記事詳細
*/
func toArticleAPIDetail(chunk postgres.MCPArticleChunk) articleAPIDetail {
	article := chunk.StoredArticle
	effectiveAt := article.FetchedAt
	if article.PublishedAt != nil {
		effectiveAt = *article.PublishedAt
	}
	return articleAPIDetail{
		ID:             article.ID,
		SourceID:       article.SourceID,
		SourceName:     article.SourceName,
		Provider:       article.Provider,
		ExternalID:     article.ExternalID,
		RevisionID:     article.RevisionID,
		CanonicalURL:   article.CanonicalURL,
		PublishedAt:    formatArticleAPIOptionalTime(article.PublishedAt),
		UpdatedAt:      formatArticleAPIOptionalTime(article.UpdatedAt),
		Headline:       article.Headline,
		BodyText:       article.BodyText,
		BodyOffset:     chunk.BodyOffset,
		HasMore:        chunk.HasMore,
		NextBodyOffset: chunk.NextBodyOffset,
		FetchedAt:      formatArticleAPIRequiredTime(article.FetchedAt),
		EffectiveAt:    formatArticleAPIRequiredTime(effectiveAt),
	}
}

// ----------------------------------------

/*
formatArticleAPIOptionalTime は、省略可能な日時を記事API向け文字列へ変換します。

機能:
  - nilを空文字へ変換し、設定済み日時をUTCへ統一する

引数:
  - value *time.Time: 変換する省略可能な日時

返り値:
  - string: RFC3339 Nano形式のUTC日時、または空文字
*/
func formatArticleAPIOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatArticleAPIRequiredTime(*value)
}

// ----------------------------------------

/*
formatArticleAPIRequiredTime は、必須日時を記事API向け文字列へ変換します。

機能:
  - ゼロ日時を空文字へ変換し、設定済み日時をUTCへ統一する

引数:
  - value time.Time: 変換する日時

返り値:
  - string: RFC3339 Nano形式のUTC日時、または空文字
*/
func formatArticleAPIRequiredTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// ----------------------------------------

/*
respondArticleAPIError は、内部情報を含まない記事APIエラーを返します。

機能:
  - エラー応答をキャッシュ禁止にしてJSON形式で返す

引数:
  - c *gin.Context: HTTPレスポンスを保持するGinコンテキスト
  - status int: HTTP状態コード
  - message string: 利用者へ公開してよいエラーメッセージ

返り値:
  - なし
*/
func respondArticleAPIError(c *gin.Context, status int, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": message})
}

// ----------------------------------------

/*
listArticles は、保存済み記事を検索・ページングして表示します。

機能:
  - 記事一覧APIをAxiosから利用するための空の画面シェルを表示する
  - 記事データや取得対象はサーバー側で読み込まない
  - 最大件数欄へ設定済みの既定件数を初期表示する

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) listArticles(c *gin.Context) {
	c.HTML(http.StatusOK, "articles.html", articleListPage{
		Base:                s.newBasePage(c, "記事", "articles"),
		DefaultArticleLimit: s.defaultArticleLimit,
	})
}

// ----------------------------------------

/*
showArticle は、指定された保存済み記事の詳細を表示します。

機能:
  - 正の記事IDを検証する
  - 記事詳細APIをAxiosから利用するための空の画面シェルを表示する
  - 記事本文やメタデータはサーバー側で読み込まない

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) showArticle(c *gin.Context) {
	if _, err := parsePathID(c, "id"); err != nil {
		s.renderNotFound(c)
		return
	}

	c.HTML(http.StatusOK, "article.html", articleDetailPage{
		Base: s.newBasePage(c, "記事詳細", "articles"),
	})
}
