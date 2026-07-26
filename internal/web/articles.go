package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

const articlesPerPage = 50

// articleListPage は、記事一覧テンプレートへ渡す検索結果とページ情報です。
type articleListPage struct {
	Base        basePage
	Page        postgres.ArticlePage
	Sources     []domain.Source
	SourceID    int64
	Keyword     string
	CurrentPage int
	PreviousURL string
	NextURL     string
}

// articleDetailPage は、記事詳細テンプレートへ渡す保存済み記事です。
type articleDetailPage struct {
	Base    basePage
	Article postgres.StoredArticle
}

/*
listArticles は、保存済み記事を検索・ページングして表示します。

機能:
  - 取得対象 ID とキーワードをクエリから解析する
  - 記事ページと取得対象選択肢を Repository から読み込む
  - 前後ページ URL を検索条件付きで生成する

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) listArticles(c *gin.Context) {
	sourceID := parseOptionalPositiveInt64(c.Query("source_id"))
	keyword := strings.TrimSpace(c.Query("q"))
	currentPage := parsePositivePage(c.Query("page"))
	offset := (currentPage - 1) * articlesPerPage

	page, err := s.store.ListArticles(c.Request.Context(), postgres.ArticleQuery{
		SourceID: sourceID,
		Keyword:  keyword,
		Limit:    articlesPerPage,
		Offset:   offset,
	})
	if err != nil {
		s.renderInternalError(c, "記事を表示できません", err)
		return
	}
	sources, err := s.store.ListSources(c.Request.Context())
	if err != nil {
		s.renderInternalError(c, "取得対象を表示できません", err)
		return
	}

	data := articleListPage{
		Base:        s.newBasePage(c, "記事", "articles"),
		Page:        page,
		Sources:     sources,
		SourceID:    sourceID,
		Keyword:     keyword,
		CurrentPage: currentPage,
	}
	if currentPage > 1 {
		data.PreviousURL = buildArticleListURL(sourceID, keyword, currentPage-1)
	}
	if offset+len(page.Items) < page.Total {
		data.NextURL = buildArticleListURL(sourceID, keyword, currentPage+1)
	}
	c.HTML(http.StatusOK, "articles.html", data)
}

// ----------------------------------------

/*
showArticle は、指定された保存済み記事の詳細を表示します。

機能:
  - 記事 ID から見出し、本文、取得元情報を読み込む
  - 本文はテンプレートの自動エスケープでプレーンテキスト表示する

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) showArticle(c *gin.Context) {
	id, err := parsePathID(c, "id")
	if err != nil {
		s.renderNotFound(c)
		return
	}
	article, err := s.store.GetArticle(c.Request.Context(), id)
	if errors.Is(err, postgres.ErrNotFound) {
		s.renderNotFound(c)
		return
	}
	if err != nil {
		s.renderInternalError(c, "記事を表示できません", err)
		return
	}

	c.HTML(http.StatusOK, "article.html", articleDetailPage{
		Base:    s.newBasePage(c, article.Headline, "articles"),
		Article: article,
	})
}

// ----------------------------------------

/*
parseOptionalPositiveInt64 は、空を許容する正整数クエリを解析します。

機能:
  - 空または不正値を絞り込みなしの 0 として扱う

引数:
  - value string: 解析するクエリ文字列

返り値:
  - int64: 正整数、または絞り込みなしを表す 0
*/
func parseOptionalPositiveInt64(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 1 {
		return 0
	}
	return parsed
}

// ----------------------------------------

/*
parsePositivePage は、ページ番号クエリを正整数へ補正します。

機能:
  - 空、不正値、1 未満を先頭ページの 1 として扱う

引数:
  - value string: 解析するページ番号

返り値:
  - int: 1 以上のページ番号
*/
func parsePositivePage(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 {
		return 1
	}
	return parsed
}

// ----------------------------------------

/*
buildArticleListURL は、検索条件を保持した記事一覧 URL を生成します。

機能:
  - 取得対象、キーワード、ページ番号を URL エンコードする
  - 未指定条件をクエリから省略する

引数:
  - sourceID int64: 絞り込む取得対象 ID。0 は指定なし
  - keyword string: 見出しまたは本文の検索語
  - page int: 遷移先ページ番号

返り値:
  - string: 管理画面の記事一覧 URL
*/
func buildArticleListURL(sourceID int64, keyword string, page int) string {
	values := url.Values{}
	if sourceID > 0 {
		values.Set("source_id", strconv.FormatInt(sourceID, 10))
	}
	if keyword != "" {
		values.Set("q", keyword)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if len(values) == 0 {
		return "/admin/articles"
	}
	return "/admin/articles?" + values.Encode()
}
