-- 更新日時、公開日時、初回取得日時の順で保存期限を効率的に判定する。
CREATE INDEX news_articles_retention_idx
    ON news_articles ((COALESCE(updated_at, published_at, fetched_at)));
