CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS news_articles_headline_trgm_idx
    ON news_articles USING GIN (headline gin_trgm_ops);

CREATE INDEX IF NOT EXISTS news_articles_body_text_trgm_idx
    ON news_articles USING GIN (body_text gin_trgm_ops);
