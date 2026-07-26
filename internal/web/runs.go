package web

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
)

// runListPage は、取得履歴一覧テンプレートへ渡す情報です。
type runListPage struct {
	Base basePage
	Runs []postgres.IngestionRun
}

/*
listRuns は、新しい順に記事取得履歴を表示します。

機能:
  - 直近 200 件の状態、件数、警告概要を Repository から読み込む

引数:
  - c *gin.Context: HTTP リクエストとレスポンスのコンテキスト

返り値:
  - なし
*/
func (s *Server) listRuns(c *gin.Context) {
	runs, err := s.store.ListRuns(c.Request.Context(), 200)
	if err != nil {
		s.renderInternalError(c, "取得履歴を表示できません", err)
		return
	}
	c.HTML(http.StatusOK, "runs.html", runListPage{
		Base: s.newBasePage(c, "取得履歴", "runs"),
		Runs: runs,
	})
}
