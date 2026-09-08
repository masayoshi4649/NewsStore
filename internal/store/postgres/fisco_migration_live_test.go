package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/masayoshi4649/NewsStore/internal/domain"
)

/*
TestLiveFiscoConnectorMigration は、Fisco 追加時の既存 DB の移行を検証します。

機能:
  - 環境変数で有効化した場合だけ独立スキーマで移行する
  - 既存の RSS と Reuters の設定を保持し、Fisco の作成と更新を許可する
  - 未対応の種別を引き続き拒否し、移行の再実行を許可する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLiveFiscoConnectorMigration(t *testing.T) {
	store := newArticleIdentityLiveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rssID := insertArticleIdentityLiveSource(t, ctx, store, "既存 RSS")
	reutersID := insertArticleIdentityLiveSource(t, ctx, store, "既存 Reuters")
	if _, err := store.db.ExecContext(ctx,
		`UPDATE news_sources SET kind = 'reuters' WHERE id = $1`, reutersID); err != nil {
		t.Fatalf("既存 Reuters の設定を準備できません: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Fisco 対応へ移行できません: %v", err)
	}
	for id, kind := range map[int64]domain.SourceKind{
		rssID: domain.SourceKindRSS, reutersID: domain.SourceKindReuters,
	} {
		source, err := store.GetSource(ctx, id)
		if err != nil || source.Kind != kind || source.EndpointURL != "https://example.com/feed" {
			t.Errorf("既存の取得対象が保持されていません: ID=%d 種別=%q エラー=%v", id, source.Kind, err)
		}
	}

	// ----------------------------------------

	source, err := store.CreateSource(ctx, domain.Source{
		Name:                 "Fisco",
		Kind:                 domain.SourceKindFisco,
		EndpointURL:          "https://web.fisco.jp/FiscoWeb/news",
		Enabled:              true,
		FetchIntervalMinutes: 15,
		MaxItems:             10,
		Settings:             []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("Fisco の取得対象を保存できません: %v", err)
	}
	source.MaxItems = 50
	if _, err := store.UpdateSource(ctx, source); err != nil {
		t.Fatalf("Fisco の最大取得件数を更新できません: %v", err)
	}
	stored, err := store.GetSource(ctx, source.ID)
	if err != nil || stored.Kind != domain.SourceKindFisco || stored.MaxItems != 50 || string(stored.Settings) != "{}" {
		t.Errorf("Fisco の設定が保存されていません: 種別=%q 件数=%d エラー=%v", stored.Kind, stored.MaxItems, err)
	}
	_, err = store.db.ExecContext(ctx, `UPDATE news_sources SET kind = 'unsupported' WHERE id = $1`, source.ID)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
		t.Errorf("未対応の種別が検査制約で拒否されません: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("適用済み移行の再実行に失敗しました: %v", err)
	}
}
