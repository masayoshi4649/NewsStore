// Package web は、Gin を使用する NewsStore 管理画面とヘルスチェックを提供します。
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
	webassets "github.com/masayoshi4649/NewsStore/web"
)

const (
	csrfCookieName = "newsstore_csrf"
	csrfFormName   = "_csrf"
	csrfContextKey = "csrf_token"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Queue は、管理画面から手動取得をキューへ追加する操作を表します。
type Queue interface {
	Enqueue(sourceID int64, triggerType string) (bool, error)
}

// Server は、管理画面が利用する依存関係と Gin エンジンを保持します。
type Server struct {
	store     *postgres.Store
	queue     Queue
	logger    *slog.Logger
	template  *template.Template
	staticCSS []byte
	staticJS  []byte
	libraryFS http.FileSystem
	engine    *gin.Engine
}

/*
New は、NewsStore の Gin 管理画面を生成します。

機能:
  - 埋め込みテンプレート、補助CSS・JavaScript、web/libのローカルライブラリを読み込む
  - セキュリティヘッダーとCSRF保護を設定する
  - 取得対象、記事、取得履歴、ヘルスチェックのルートを登録する

引数:
  - store *postgres.Store: 管理画面が操作する PostgreSQL Repository
  - queue Queue: 手動取得を受け付けるジョブキュー
  - logger *slog.Logger: サーバーエラーを記録するロガー

返り値:
  - *Server: 初期化済みの Web サーバー
  - error: テンプレートまたは静的ファイルの読込に失敗した場合のエラー
*/
func New(
	store *postgres.Store,
	queue Queue,
	logger *slog.Logger,
) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	parsedTemplate, err := template.New("管理画面").Funcs(template.FuncMap{
		"formatTime":  formatTime,
		"shorten":     shorten,
		"statusLabel": statusLabel,
		"kindLabel":   kindLabel,
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("管理画面テンプレートを解析できません: %w", err)
	}
	staticCSS, err := assets.ReadFile("static/app.css")
	if err != nil {
		return nil, fmt.Errorf("管理画面 CSS を読み込めません: %w", err)
	}
	staticJS, err := assets.ReadFile("static/source-form.js")
	if err != nil {
		return nil, fmt.Errorf("取得対象フォーム JavaScript を読み込めません: %w", err)
	}
	libraryFS, err := webassets.FileSystem()
	if err != nil {
		return nil, err
	}

	server := &Server{
		store:     store,
		queue:     queue,
		logger:    logger,
		template:  parsedTemplate,
		staticCSS: staticCSS,
		staticJS:  staticJS,
		libraryFS: libraryFS,
	}
	server.engine = server.buildEngine()
	return server, nil
}

// ----------------------------------------

/*
Handler は、標準 HTTP サーバーへ渡せる Gin エンジンを返します。

機能:
  - Server が構築したルーターを http.Handler として公開する

引数:
  - なし

返り値:
  - http.Handler: 管理画面とヘルスチェックを処理するハンドラー
*/
func (s *Server) Handler() http.Handler {
	return s.engine
}

// ----------------------------------------

/*
buildEngine は、管理画面のミドルウェアとルートを組み立てます。

機能:
  - Gin のリカバリーとセキュリティヘッダーを有効にする
  - トップページを取得対象一覧へ転送する
  - 公開ヘルスチェック、補助CSS・JavaScript、ローカルWebライブラリを登録する
  - 管理画面の状態変更へCSRF保護を適用する

引数:
  - なし

返り値:
  - *gin.Engine: ルート登録済みの Gin エンジン
*/
func (s *Server) buildEngine() *gin.Engine {
	engine := gin.New()
	engine.Use(gin.Recovery(), securityHeaders())
	engine.SetHTMLTemplate(s.template)

	engine.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusSeeOther, "/admin/sources")
	})
	engine.GET("/healthz", s.health)
	engine.GET("/static/app.css", s.css)
	engine.GET("/static/source-form.js", s.sourceFormJavaScript)
	engine.StaticFS("/lib", s.libraryFS)

	admin := engine.Group("/admin")
	admin.Use(s.csrf())
	admin.GET("", func(c *gin.Context) {
		c.Redirect(http.StatusSeeOther, "/admin/sources")
	})
	admin.GET("/sources", s.listSources)
	admin.GET("/sources/new", s.newSource)
	admin.POST("/sources", s.createSource)
	admin.GET("/sources/:id/edit", s.editSource)
	admin.POST("/sources/:id", s.updateSource)
	admin.POST("/sources/:id/delete", s.deleteSource)
	admin.POST("/sources/:id/fetch", s.fetchSource)
	admin.GET("/articles", s.listArticles)
	admin.GET("/articles/:id", s.showArticle)
	admin.GET("/runs", s.listRuns)

	engine.NoRoute(func(c *gin.Context) {
		c.String(http.StatusNotFound, "ページが見つかりません")
	})
	return engine
}

// ----------------------------------------

/*
health は、アプリと PostgreSQL の稼働状態を返します。

機能:
  - PostgreSQL へ Ping を行う
  - 成功時は 200、失敗時は 503 の JSON を返す

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) health(c *gin.Context) {
	if err := s.store.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":   "unavailable",
			"database": "unavailable",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"database": "ok",
	})
}

// ----------------------------------------

/*
css は、バイナリへ埋め込んだ管理画面 CSS を返します。

機能:
  - Content-Type とキャッシュ制御を設定して CSS を配信する

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) css(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "text/css; charset=utf-8", s.staticCSS)
}

// ----------------------------------------

/*
sourceFormJavaScript は、取得元種別で設定欄を切り替えるJavaScriptを返します。

機能:
  - Content-Typeとキャッシュ制御を設定して埋め込みJavaScriptを配信する

引数:
  - c *gin.Context: HTTPリクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) sourceFormJavaScript(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "text/javascript; charset=utf-8", s.staticJS)
}

// ----------------------------------------

/*
csrf は、管理画面の状態変更リクエストを CSRF から保護します。

機能:
  - 暗号学的乱数から Cookie トークンを生成する
  - POST の Cookie とフォームトークンを定数時間で比較する
  - テンプレートが利用するトークンを Gin コンテキストへ設定する

引数:
  - なし

返り値:
  - gin.HandlerFunc: 管理画面グループへ適用するミドルウェア
*/
func (s *Server) csrf() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(csrfCookieName)
		if err != nil || token == "" {
			token, err = newCSRFToken()
			if err != nil {
				s.logger.Error("CSRF トークンを生成できません", "error", err)
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			http.SetCookie(c.Writer, &http.Cookie{
				Name:     csrfCookieName,
				Value:    token,
				Path:     "/admin",
				MaxAge:   12 * 60 * 60,
				HttpOnly: true,
				Secure:   c.Request.TLS != nil,
				SameSite: http.SameSiteStrictMode,
			})
		}
		c.Set(csrfContextKey, token)

		if c.Request.Method == http.MethodPost {
			submitted := c.PostForm(csrfFormName)
			if subtle.ConstantTimeCompare([]byte(token), []byte(submitted)) != 1 {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": "CSRF トークンが一致しません",
				})
				return
			}
		}
		c.Next()
	}
}

// ----------------------------------------

/*
securityHeaders は、ブラウザー向けの基本セキュリティヘッダーを設定します。

機能:
  - MIME 推測、フレーム埋め込み、不要なリファラー送信を抑止する
  - 管理画面の読み込み元を同一オリジンへ制限する

引数:
  - なし

返り値:
  - gin.HandlerFunc: 全ルートへ適用するミドルウェア
*/
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header(
			"Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'",
		)
		c.Next()
	}
}

// ----------------------------------------

/*
newCSRFToken は、CSRF 検証用の予測困難なトークンを生成します。

機能:
  - crypto/rand で 32 バイトを読み取る
  - Cookie とフォームへ安全に入る URL セーフ Base64 へ変換する

引数:
  - なし

返り値:
  - string: 生成した CSRF トークン
  - error: 乱数生成に失敗した場合のエラー
*/
func newCSRFToken() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("安全な乱数を生成できません: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

// ----------------------------------------

/*
csrfToken は、テンプレートへ渡す CSRF トークンを取得します。

機能:
  - Gin コンテキストの値を文字列として安全に取り出す

引数:
  - c *gin.Context: CSRF ミドルウェア適用済みのコンテキスト

返り値:
  - string: フォーム hidden 項目へ設定するトークン
*/
func csrfToken(c *gin.Context) string {
	value, exists := c.Get(csrfContextKey)
	if !exists {
		return ""
	}
	token, _ := value.(string)
	return token
}

// ----------------------------------------

/*
formatTime は、テンプレート上の日時を日本時間の読みやすい形式へ変換します。

機能:
  - time.Time と *time.Time の両方を処理する
  - nil またはゼロ日時をハイフンとして表示する

引数:
  - value any: time.Time、*time.Time、またはその他の値

返り値:
  - string: Asia/Tokyo で整形した日時またはハイフン
*/
func formatTime(value any) string {
	var target time.Time
	switch typed := value.(type) {
	case time.Time:
		target = typed
	case *time.Time:
		if typed == nil {
			return "—"
		}
		target = *typed
	default:
		return "—"
	}
	if target.IsZero() {
		return "—"
	}
	location, err := time.LoadLocation("Asia/Tokyo")
	if err == nil {
		target = target.In(location)
	}
	return target.Format("2006-01-02 15:04:05")
}

// ----------------------------------------

/*
shorten は、一覧表示用に文字列を指定ルーン数へ短縮します。

機能:
  - UTF-8 文字を壊さず末尾へ省略記号を付ける

引数:
  - value string: 短縮する文字列
  - max int: 表示する最大ルーン数

返り値:
  - string: 上限以内の文字列
*/
func shorten(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if max < 1 || len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

// ----------------------------------------

/*
statusLabel は、取得履歴の状態を日本語表示へ変換します。

機能:
  - DB 状態値を管理画面向けラベルへ対応付ける

引数:
  - status postgres.RunStatus: 取得履歴の状態

返り値:
  - string: 日本語の状態ラベル
*/
func statusLabel(status postgres.RunStatus) string {
	switch status {
	case postgres.RunStatusRunning:
		return "実行中"
	case postgres.RunStatusSucceeded:
		return "成功"
	case postgres.RunStatusPartial:
		return "一部警告"
	case postgres.RunStatusFailed:
		return "失敗"
	default:
		return string(status)
	}
}

// ----------------------------------------

/*
kindLabel は、取得対象種別を管理画面向け表示へ変換します。

機能:
  - rss と reuters を利用者向けラベルへ対応付ける

引数:
  - kind any: 文字列化できる取得対象種別

返り値:
  - string: 取得対象種別の表示名
*/
func kindLabel(kind any) string {
	switch fmt.Sprint(kind) {
	case "rss":
		return "RSS / Atom"
	case "reuters":
		return "Reuters"
	default:
		return fmt.Sprint(kind)
	}
}

// ----------------------------------------

/*
parsePathID は、Gin パスパラメーターの正整数 ID を解析します。

機能:
  - 指定キーの文字列を 64 ビット整数へ変換する
  - 1 未満の値を不正として扱う

引数:
  - c *gin.Context: パスパラメーターを持つ Gin コンテキスト
  - name string: 読み取るパラメーター名

返り値:
  - int64: 解析した正整数 ID
  - error: 数値でない場合または 1 未満の場合のエラー
*/
func parsePathID(c *gin.Context, name string) (int64, error) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("不正な ID です")
	}
	return id, nil
}
