package postgres

import (
	"regexp"
	"strings"
	"testing"
)

/*
TestSourceDisplayOrderMigrationPreservesExistingListOrder は、取得対象表示順序マイグレーションを検証します。

機能:
  - display_order が BIGINT NOT NULL と最大値の既定値で追加されることを確認する
  - 既存未削除行を従来の管理一覧順で連番化することを確認する
  - 未削除行向けの表示順序部分インデックスが作成されることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestSourceDisplayOrderMigrationPreservesExistingListOrder(t *testing.T) {
	t.Parallel()

	body, err := migrationFiles.ReadFile("migrations/004_source_display_order.sql")
	if err != nil {
		t.Fatalf("表示順序マイグレーションを読み込めません: %v", err)
	}
	migration := string(body)

	requiredPatterns := []string{
		`(?i)ADD\s+COLUMN\s+display_order\s+BIGINT\s+NOT\s+NULL\s+DEFAULT\s+9223372036854775807`,
		`(?is)ROW_NUMBER\(\)\s+OVER\s*\(\s*ORDER\s+BY\s+enabled\s+DESC,\s*LOWER\(name\),\s*id\s*\)`,
		`(?is)FROM\s+news_sources\s+WHERE\s+deleted_at\s+IS\s+NULL`,
		`(?is)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+news_sources_display_order_idx\s+ON\s+news_sources\s*\(display_order,\s*id\)\s+WHERE\s+deleted_at\s+IS\s+NULL`,
	}
	for _, pattern := range requiredPatterns {
		if !regexp.MustCompile(pattern).MatchString(migration) {
			t.Errorf("表示順序マイグレーションに必要な定義がありません: %s", pattern)
		}
	}
	if strings.Contains(migration, "ALTER COLUMN display_order DROP DEFAULT") {
		t.Error("新規取得対象を末尾へ配置する既定値が削除されています")
	}
}
