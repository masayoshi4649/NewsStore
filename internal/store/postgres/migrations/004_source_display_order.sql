ALTER TABLE news_sources
    ADD COLUMN display_order BIGINT NOT NULL DEFAULT 9223372036854775807;

WITH ranked_sources AS (
    SELECT
        id,
        ROW_NUMBER() OVER (
            ORDER BY enabled DESC, LOWER(name), id
        ) AS display_order
    FROM news_sources
    WHERE deleted_at IS NULL
)
UPDATE news_sources AS source
SET display_order = ranked.display_order
FROM ranked_sources AS ranked
WHERE source.id = ranked.id;

CREATE INDEX IF NOT EXISTS news_sources_display_order_idx
    ON news_sources (display_order, id)
    WHERE deleted_at IS NULL;
