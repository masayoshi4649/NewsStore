package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/connector"
	"github.com/masayoshi4649/NewsStore/internal/connector/reuters"
	"github.com/masayoshi4649/NewsStore/internal/domain"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

// basePage は、すべての管理画面へ渡す共通表示情報です。
type basePage struct {
	Title     string
	CSRFToken string
	Message   string
	Error     string
	Active    string
}

// sourceForm は、取得対象の編集フォームへ表示する値です。
type sourceForm struct {
	ID                   int64
	Name                 string
	Kind                 domain.SourceKind
	EndpointURL          string
	Enabled              bool
	FetchIntervalMinutes int
	MaxItems             int
	FetchFullArticle     bool
	ReutersTarget        string
}

// sourceListItem は、取得対象と一覧画面向けの説明をまとめます。
type sourceListItem struct {
	domain.Source
	Description string
}

// sourceListPage は、取得対象一覧テンプレートへ渡す情報です。
type sourceListPage struct {
	Base    basePage
	Sources []sourceListItem
}

// sourceFormPage は、取得対象フォームへ渡す値と検証エラーです。
type sourceFormPage struct {
	Base           basePage
	Form           sourceForm
	ReutersTargets []reuters.Target
	IsEdit         bool
}

/*
listSources は、取得対象の管理一覧を表示します。

機能:
  - 論理削除されていない取得対象を Repository から読み込む
  - 実行状態と次回予定をテンプレートへ渡す

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) listSources(c *gin.Context) {
	sources, err := s.store.ListSources(c.Request.Context())
	if err != nil {
		s.renderInternalError(c, "取得対象を表示できません", err)
		return
	}
	c.HTML(http.StatusOK, "sources.html", sourceListPage{
		Base:    s.newBasePage(c, "取得対象", "sources"),
		Sources: buildSourceListItems(sources),
	})
}

// ----------------------------------------

/*
buildSourceListItems は、取得対象へ種類別の一覧説明を付与します。

機能:
  - RSSはフィードURLを説明として使用する
  - Reutersは固定URLではなく選択された記事分野を表示する

引数:
  - sources []domain.Source: Repositoryから読み込んだ取得対象一覧

返り値:
  - []sourceListItem: 種類別の説明を付けた表示用一覧
*/
func buildSourceListItems(sources []domain.Source) []sourceListItem {
	items := make([]sourceListItem, 0, len(sources))
	for _, source := range sources {
		description := source.EndpointURL
		if source.Kind == domain.SourceKindReuters {
			form := sourceToForm(source)
			if target, ok := reuters.FindTarget(form.ReutersTarget); ok {
				description = "Reuters 日本語版 / " + target.Label
			} else {
				description = "Reuters 日本語版"
			}
		}
		items = append(items, sourceListItem{
			Source:      source,
			Description: description,
		})
	}
	return items
}

// ----------------------------------------

/*
updateSourceOrder は、取得対象一覧で変更された表示順を保存します。

機能:
  - フォームで送信された取得対象 ID を表示順どおりに解析する
  - Repository で未削除の取得対象全件を原子的に並べ替える
  - 別画面で一覧が更新されていた場合は再読込を促す競合応答を返す

引数:
  - c *gin.Context: 表示順と CSRF トークンを持つ HTTP コンテキスト

返り値:
  - なし
*/
func (s *Server) updateSourceOrder(c *gin.Context) {
	sourceIDs, err := parseSourceOrderIDs(c.PostFormArray("source_ids"))
	if err != nil {
		respondSourceOrderError(c, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.store.UpdateSourceOrder(c.Request.Context(), sourceIDs); err != nil {
		if errors.Is(err, postgres.ErrInvalidSourceOrder) {
			respondSourceOrderError(
				c,
				http.StatusConflict,
				"取得対象の一覧が更新されています。画面を再読み込みしてから並べ替えてください。",
			)
			return
		}
		s.logger.Error("取得対象の表示順を保存できません", "error", err)
		respondSourceOrderError(
			c,
			http.StatusInternalServerError,
			"取得対象の表示順を保存できませんでした。",
		)
		return
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"message": "表示順を保存しました。"})
}

// ----------------------------------------

/*
parseSourceOrderIDs は、表示順フォームの取得対象 ID を解析します。

機能:
  - すべての値を 1 以上の 64 ビット整数として検証する
  - 同じ取得対象 ID の重複を拒否する
  - 入力配列の順序を維持して返す

引数:
  - values []string: source_ids フォーム項目の値

返り値:
  - []int64: 表示順どおりの取得対象 ID
  - error: 空、不正な整数、または重複した ID がある場合のエラー
*/
func parseSourceOrderIDs(values []string) ([]int64, error) {
	if len(values) == 0 {
		return nil, errors.New("並べ替える取得対象が指定されていません")
	}

	sourceIDs := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id < 1 {
			return nil, fmt.Errorf("取得対象 ID が不正です: %q", value)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("取得対象 ID が重複しています: %d", id)
		}
		seen[id] = struct{}{}
		sourceIDs = append(sourceIDs, id)
	}
	return sourceIDs, nil
}

// ----------------------------------------

/*
respondSourceOrderError は、表示順保存 API のエラーを JSON で返します。

機能:
  - 応答をキャッシュ禁止にする
  - 利用者へ表示できる安全なメッセージだけを返す

引数:
  - c *gin.Context: HTTP レスポンスを返す Gin コンテキスト
  - status int: HTTP 状態コード
  - message string: 利用者へ表示するエラーメッセージ

返り値:
  - なし
*/
func respondSourceOrderError(c *gin.Context, status int, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": message})
}

// ----------------------------------------

/*
newSource は、新しい取得対象の入力フォームを表示します。

機能:
  - 実用的な取得間隔と件数の既定値をフォームへ設定する

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) newSource(c *gin.Context) {
	c.HTML(http.StatusOK, "source_form.html", sourceFormPage{
		Base: s.newBasePage(c, "取得対象を追加", "sources"),
		Form: sourceForm{
			Kind:                 domain.SourceKindRSS,
			Enabled:              true,
			FetchIntervalMinutes: 15,
			MaxItems:             20,
			FetchFullArticle:     false,
			ReutersTarget:        reuters.DefaultTargetID,
		},
		ReutersTargets: reuters.Targets(),
	})
}

// ----------------------------------------

/*
createSource は、入力された取得対象を検証して保存します。

機能:
  - フォーム値を共通取得対象とコネクタ固有 JSON へ変換する
  - 不正値は入力を保持したままフォームへ表示する
  - 保存成功後は取得対象一覧へリダイレクトする

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) createSource(c *gin.Context) {
	form, source, err := parseSourceForm(c, 0)
	if err == nil {
		err = connector.ValidateSource(source)
	}
	if err != nil {
		c.HTML(http.StatusBadRequest, "source_form.html", sourceFormPage{
			Base: basePage{
				Title:     "取得対象を追加",
				CSRFToken: csrfToken(c),
				Error:     err.Error(),
				Active:    "sources",
			},
			Form:           form,
			ReutersTargets: reuters.Targets(),
		})
		return
	}

	if _, err := s.store.CreateSource(c.Request.Context(), source); err != nil {
		s.logger.Error("取得対象を作成できません", "error", err)
		c.HTML(http.StatusBadRequest, "source_form.html", sourceFormPage{
			Base: basePage{
				Title:     "取得対象を追加",
				CSRFToken: csrfToken(c),
				Error:     "取得対象を保存できません。名前の重複と入力値を確認してください。",
				Active:    "sources",
			},
			Form:           form,
			ReutersTargets: reuters.Targets(),
		})
		return
	}
	redirectWithMessage(c, "/admin/sources", "取得対象を追加しました")
}

// ----------------------------------------

/*
editSource は、既存取得対象の編集フォームを表示します。

機能:
  - DB の共通項目と settings JSON をフォーム値へ変換する

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) editSource(c *gin.Context) {
	id, err := parsePathID(c, "id")
	if err != nil {
		s.renderNotFound(c)
		return
	}
	source, err := s.store.GetSource(c.Request.Context(), id)
	if errors.Is(err, postgres.ErrNotFound) {
		s.renderNotFound(c)
		return
	}
	if err != nil {
		s.renderInternalError(c, "取得対象を表示できません", err)
		return
	}

	c.HTML(http.StatusOK, "source_form.html", sourceFormPage{
		Base:           s.newBasePage(c, "取得対象を編集", "sources"),
		Form:           sourceToForm(source),
		ReutersTargets: reuters.Targets(),
		IsEdit:         true,
	})
}

// ----------------------------------------

/*
updateSource は、既存取得対象を検証して更新します。

機能:
  - パス ID とフォーム値から更新後の取得対象を構築する
  - 検証失敗時は入力を保持したフォームを返す
  - 更新成功後は取得対象一覧へリダイレクトする

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) updateSource(c *gin.Context) {
	id, err := parsePathID(c, "id")
	if err != nil {
		s.renderNotFound(c)
		return
	}
	form, source, err := parseSourceForm(c, id)
	if err == nil {
		err = connector.ValidateSource(source)
	}
	if err != nil {
		c.HTML(http.StatusBadRequest, "source_form.html", sourceFormPage{
			Base: basePage{
				Title:     "取得対象を編集",
				CSRFToken: csrfToken(c),
				Error:     err.Error(),
				Active:    "sources",
			},
			Form:           form,
			ReutersTargets: reuters.Targets(),
			IsEdit:         true,
		})
		return
	}

	if _, err := s.store.UpdateSource(c.Request.Context(), source); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			s.renderNotFound(c)
			return
		}
		s.logger.Error("取得対象を更新できません", "source_id", id, "error", err)
		c.HTML(http.StatusBadRequest, "source_form.html", sourceFormPage{
			Base: basePage{
				Title:     "取得対象を編集",
				CSRFToken: csrfToken(c),
				Error:     "取得対象を更新できません。名前の重複と入力値を確認してください。",
				Active:    "sources",
			},
			Form:           form,
			ReutersTargets: reuters.Targets(),
			IsEdit:         true,
		})
		return
	}
	redirectWithMessage(c, "/admin/sources", "取得対象を更新しました")
}

// ----------------------------------------

/*
deleteSource は、取得対象を論理削除します。

機能:
  - 記事と取得履歴を保持したまま定期取得を停止する
  - 削除成功後は一覧へリダイレクトする

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) deleteSource(c *gin.Context) {
	id, err := parsePathID(c, "id")
	if err != nil {
		s.renderNotFound(c)
		return
	}
	if err := s.store.DeleteSource(c.Request.Context(), id); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			s.renderNotFound(c)
			return
		}
		s.renderInternalError(c, "取得対象を削除できません", err)
		return
	}
	redirectWithMessage(c, "/admin/sources", "取得対象を削除しました。記事履歴は保持されています")
}

// ----------------------------------------

/*
fetchSource は、取得対象を手動取得キューへ追加します。

機能:
  - ID が存在することを確認する
  - HTTP リクエスト内では取得せず、共通ワーカーへ manual ジョブを渡す

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) fetchSource(c *gin.Context) {
	id, err := parsePathID(c, "id")
	if err != nil {
		s.renderNotFound(c)
		return
	}
	if _, err := s.store.GetSource(c.Request.Context(), id); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			s.renderNotFound(c)
			return
		}
		s.renderInternalError(c, "取得対象を確認できません", err)
		return
	}

	added, err := s.queue.Enqueue(id, "manual")
	if err != nil {
		redirectWithMessage(c, "/admin/sources", "取得キューへ追加できません: "+err.Error())
		return
	}
	if !added {
		redirectWithMessage(c, "/admin/sources", "この取得対象は既に待機中または実行中です")
		return
	}
	redirectWithMessage(c, "/admin/runs", "手動取得をキューへ追加しました")
}

// ----------------------------------------

/*
parseSourceForm は、HTTP フォームを取得対象と表示用フォームへ変換します。

機能:
  - 数値項目を範囲検証可能な整数へ解析する
  - 種別ごとの settings JSON を生成する
  - RSSでは入力URL、Reutersでは定義済み固定URLを使用する
  - Reutersの取得内容をホワイトリストで検証する

引数:
  - c *gin.Context: POST フォームを持つ Gin コンテキスト
  - id int64: 新規作成時は 0、更新時は取得対象 ID

返り値:
  - sourceForm: 入力内容を保持する表示用フォーム
  - domain.Source: Repository へ渡す取得対象
  - error: 数値または JSON の変換に失敗した場合のエラー
*/
func parseSourceForm(c *gin.Context, id int64) (sourceForm, domain.Source, error) {
	form := sourceForm{
		ID:               id,
		Name:             strings.TrimSpace(c.PostForm("name")),
		Kind:             domain.SourceKind(strings.TrimSpace(c.PostForm("kind"))),
		EndpointURL:      strings.TrimSpace(c.PostForm("endpoint_url")),
		Enabled:          c.PostForm("enabled") == "on",
		FetchFullArticle: c.PostForm("fetch_full_article") == "on",
		ReutersTarget:    strings.TrimSpace(c.PostForm("reuters_target")),
	}

	interval, err := parseFormInteger(c.PostForm("fetch_interval_minutes"), "取得間隔")
	if err != nil {
		return form, domain.Source{}, err
	}
	form.FetchIntervalMinutes = interval
	maxItems, err := parseFormInteger(c.PostForm("max_items"), "最大取得件数")
	if err != nil {
		return form, domain.Source{}, err
	}
	form.MaxItems = maxItems

	if form.Kind == domain.SourceKindReuters {
		if form.ReutersTarget == "" {
			form.ReutersTarget = reuters.DefaultTargetID
		}
		if _, ok := reuters.FindTarget(form.ReutersTarget); !ok {
			return form, domain.Source{}, fmt.Errorf(
				"Reutersの取得内容が未対応です: %q",
				form.ReutersTarget,
			)
		}
		form.EndpointURL = reuters.EndpointURL
	}

	settings, err := sourceSettings(form)
	if err != nil {
		return form, domain.Source{}, err
	}
	return form, domain.Source{
		ID:                   id,
		Name:                 form.Name,
		Kind:                 form.Kind,
		EndpointURL:          form.EndpointURL,
		Enabled:              form.Enabled,
		FetchIntervalMinutes: form.FetchIntervalMinutes,
		MaxItems:             form.MaxItems,
		Settings:             settings,
	}, nil
}

// ----------------------------------------

/*
sourceSettings は、フォームからコネクタ固有 JSON を生成します。

機能:
  - RSS にはリンク先本文取得設定を保存する
  - Reuters には選択肢に対応するsection_idだけを保存する

引数:
  - form sourceForm: コネクタ固有の入力値を持つフォーム

返り値:
  - json.RawMessage: PostgreSQL settings 列へ保存する JSON
  - error: JSON 変換に失敗した場合のエラー
*/
func sourceSettings(form sourceForm) (json.RawMessage, error) {
	var value any
	switch form.Kind {
	case domain.SourceKindRSS:
		value = map[string]any{
			"fetch_full_article": form.FetchFullArticle,
		}
	case domain.SourceKindReuters:
		target, ok := reuters.FindTarget(form.ReutersTarget)
		if !ok {
			return nil, fmt.Errorf("Reutersの取得内容が未対応です: %q", form.ReutersTarget)
		}
		value = map[string]any{
			"section_id": target.SectionID,
		}
	default:
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("コネクタ設定を JSON へ変換できません: %w", err)
	}
	return encoded, nil
}

// ----------------------------------------

/*
sourceToForm は、保存済み取得対象を編集フォームへ変換します。

機能:
  - 共通項目をコピーする
  - settings JSON の RSS/Reuters 項目を既定値付きで読み込む

引数:
  - source domain.Source: DB から読み込んだ取得対象

返り値:
  - sourceForm: 編集画面へ表示するフォーム値
*/
func sourceToForm(source domain.Source) sourceForm {
	form := sourceForm{
		ID:                   source.ID,
		Name:                 source.Name,
		Kind:                 source.Kind,
		EndpointURL:          source.EndpointURL,
		Enabled:              source.Enabled,
		FetchIntervalMinutes: source.FetchIntervalMinutes,
		MaxItems:             source.MaxItems,
		ReutersTarget:        reuters.DefaultTargetID,
	}
	var settings struct {
		FetchFullArticle bool   `json:"fetch_full_article"`
		SectionID        string `json:"section_id"`
	}
	if json.Unmarshal(source.Settings, &settings) == nil {
		form.FetchFullArticle = settings.FetchFullArticle
		if target, ok := reuters.FindTargetBySectionID(settings.SectionID); ok {
			form.ReutersTarget = target.ID
		}
	}
	if source.Kind == domain.SourceKindReuters {
		form.EndpointURL = reuters.EndpointURL
	}
	return form
}

// ----------------------------------------

/*
parseFormInteger は、必須フォーム項目を整数へ変換します。

機能:
  - 前後空白を除去して 10 進整数として解析する

引数:
  - value string: 解析対象のフォーム値
  - label string: エラーメッセージへ表示する項目名

返り値:
  - int: 解析した整数
  - error: 空または整数でない場合のエラー
*/
func parseFormInteger(value, label string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%sは整数で指定してください", label)
	}
	return parsed, nil
}

// ----------------------------------------

/*
newBasePage は、現在のリクエストから共通ページ情報を生成します。

機能:
  - CSRF トークン、通知メッセージ、ナビゲーション状態をまとめる

引数:
  - c *gin.Context: クエリと CSRF 値を持つ Gin コンテキスト
  - title string: ページタイトル
  - active string: 選択中ナビゲーション識別子

返り値:
  - basePage: テンプレートへ渡す共通ページ情報
*/
func (s *Server) newBasePage(c *gin.Context, title, active string) basePage {
	return basePage{
		Title:     title,
		CSRFToken: csrfToken(c),
		Message:   c.Query("message"),
		Active:    active,
	}
}

// ----------------------------------------

/*
redirectWithMessage は、通知メッセージ付きで管理画面へリダイレクトします。

機能:
  - メッセージを URL クエリとして安全にエンコードする
  - POST 後の再送信を避けるため 303 を返す

引数:
  - c *gin.Context: HTTP レスポンスを返す Gin コンテキスト
  - destination string: リダイレクト先パス
  - message string: 次画面に表示する通知

返り値:
  - なし
*/
func redirectWithMessage(c *gin.Context, destination, message string) {
	values := url.Values{}
	values.Set("message", message)
	c.Redirect(http.StatusSeeOther, destination+"?"+values.Encode())
}

// ----------------------------------------

/*
renderNotFound は、管理画面向けの 404 ページを表示します。

機能:
  - CSRF とナビゲーションを含む共通エラーテンプレートを返す

引数:
  - c *gin.Context: HTTP レスポンスを返す Gin コンテキスト

返り値:
  - なし
*/
func (s *Server) renderNotFound(c *gin.Context) {
	c.HTML(http.StatusNotFound, "error.html", struct {
		Base basePage
	}{
		Base: basePage{
			Title:     "見つかりません",
			CSRFToken: csrfToken(c),
			Error:     "指定されたデータが見つかりません。",
		},
	})
}

// ----------------------------------------

/*
renderInternalError は、詳細をログへ残して安全な 500 ページを表示します。

機能:
  - 内部エラーを構造化ログへ記録する
  - 利用者には秘密情報を含まない概要だけを返す

引数:
  - c *gin.Context: HTTP レスポンスを返す Gin コンテキスト
  - message string: 利用者へ表示する安全な概要
  - err error: ログへ記録する内部エラー

返り値:
  - なし
*/
func (s *Server) renderInternalError(c *gin.Context, message string, err error) {
	s.logger.Error(message, "error", err)
	c.HTML(http.StatusInternalServerError, "error.html", struct {
		Base basePage
	}{
		Base: basePage{
			Title:     "エラー",
			CSRFToken: csrfToken(c),
			Error:     message,
		},
	})
}
