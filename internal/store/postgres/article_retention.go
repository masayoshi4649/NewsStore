package postgres

import (
	"context"
	"fmt"
)

/*
DeleteExpiredArticles は、保持期間を過ぎた記事を削除します。

機能:
  - 更新日時、投稿日時、取得日時の優先順で保持期限を判定する
  - 判定日時から 168 時間以上が経過した記事を削除する

引数:
  - ctx context.Context: 削除処理の期限とキャンセルを伝えるコンテキスト

返り値:
  - int64: 削除した記事の件数
  - error: 記事削除または削除件数の取得に失敗した場合のエラー
*/
func (s *Store) DeleteExpiredArticles(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM news_articles
		WHERE COALESCE(updated_at, published_at, fetched_at)
			<= CURRENT_TIMESTAMP - INTERVAL '168 hours'
	`)
	if err != nil {
		return 0, fmt.Errorf("保持期間を過ぎた記事を削除できません: %w", err)
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("削除した記事の件数を取得できません: %w", err)
	}
	return deleted, nil
}
