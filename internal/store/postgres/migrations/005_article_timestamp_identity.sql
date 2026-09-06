-- 同じ取得対象・記事・更新日時の重複は、最後に取得した内容を残す。
-- 更新日時がない場合は公開日時を使い、両方未設定の場合も同じ記事を一行にする。
LOCK TABLE news_articles IN ACCESS EXCLUSIVE MODE;

WITH ranked_articles AS (
    SELECT id, ROW_NUMBER() OVER (
        PARTITION BY source_id, external_id,
            COALESCE(updated_at, published_at, '-infinity'::TIMESTAMPTZ)
        ORDER BY fetched_at DESC, id DESC
    ) AS position
    FROM news_articles
)
DELETE FROM news_articles AS article
USING ranked_articles AS ranked
WHERE article.id = ranked.id AND ranked.position > 1;

-- ----------------------------------------

-- リビジョンだけが変わっても同日時の履歴は増やさず、DB 制約で同時実行にも対応する。
ALTER TABLE news_articles DROP CONSTRAINT news_articles_source_revision_uq;

CREATE UNIQUE INDEX news_articles_source_timestamp_uq
    ON news_articles (
        source_id, external_id,
        (COALESCE(updated_at, published_at, '-infinity'::TIMESTAMPTZ))
    );
