-- 既存の取得対象を維持したまま、Fisco のコネクタ種別を許可します。
ALTER TABLE news_sources
    DROP CONSTRAINT news_sources_kind_check,
    ADD CONSTRAINT news_sources_kind_check CHECK (kind IN ('rss', 'reuters', 'fisco'));
