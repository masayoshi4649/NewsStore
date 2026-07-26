CREATE INDEX IF NOT EXISTS news_articles_effective_at_idx
    ON news_articles ((COALESCE(published_at, fetched_at)) DESC, id DESC);

CREATE INDEX IF NOT EXISTS news_articles_source_effective_at_idx
    ON news_articles (source_id, (COALESCE(published_at, fetched_at)) DESC, id DESC);
