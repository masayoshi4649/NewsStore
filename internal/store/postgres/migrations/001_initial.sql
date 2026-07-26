CREATE TABLE IF NOT EXISTS news_sources (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('rss', 'reuters')),
    endpoint_url TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    fetch_interval_minutes INTEGER NOT NULL DEFAULT 15
        CHECK (fetch_interval_minutes BETWEEN 1 AND 10080),
    max_items INTEGER NOT NULL DEFAULT 20
        CHECK (max_items BETWEEN 1 AND 50),
    settings JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(settings) = 'object'),
    last_fetched_at TIMESTAMPTZ,
    next_fetch_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS news_sources_active_name_uq
    ON news_sources (LOWER(name))
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS news_sources_due_idx
    ON news_sources (next_fetch_at)
    WHERE enabled = TRUE AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS news_articles (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES news_sources(id),
    provider TEXT NOT NULL CHECK (BTRIM(provider) <> ''),
    external_id TEXT NOT NULL CHECK (BTRIM(external_id) <> ''),
    revision_id TEXT NOT NULL CHECK (BTRIM(revision_id) <> ''),
    canonical_url TEXT NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    headline TEXT NOT NULL CHECK (BTRIM(headline) <> ''),
    body_text TEXT NOT NULL CHECK (BTRIM(body_text) <> ''),
    raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT news_articles_source_revision_uq
        UNIQUE (source_id, external_id, revision_id)
);

CREATE INDEX IF NOT EXISTS news_articles_published_idx
    ON news_articles (published_at DESC NULLS LAST, id DESC);

CREATE INDEX IF NOT EXISTS news_articles_source_published_idx
    ON news_articles (source_id, published_at DESC NULLS LAST, id DESC);

CREATE TABLE IF NOT EXISTS ingestion_runs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id BIGINT NOT NULL REFERENCES news_sources(id),
    trigger_type TEXT NOT NULL
        CHECK (trigger_type IN ('scheduled', 'manual', 'cli')),
    status TEXT NOT NULL
        CHECK (status IN ('running', 'succeeded', 'partial', 'failed')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ,
    fetched_count INTEGER NOT NULL DEFAULT 0 CHECK (fetched_count >= 0),
    inserted_count INTEGER NOT NULL DEFAULT 0 CHECK (inserted_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    warning_count INTEGER NOT NULL DEFAULT 0 CHECK (warning_count >= 0),
    error_message TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS ingestion_runs_source_started_idx
    ON ingestion_runs (source_id, started_at DESC, id DESC);
