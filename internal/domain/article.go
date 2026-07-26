package domain

import (
	"encoding/json"
	"time"
)

// Article は、取得元から収集した見出しと本文を含む記事を表します。
type Article struct {
	SourceID     int64           `json:"source_id"`
	Provider     string          `json:"provider"`
	ExternalID   string          `json:"external_id"`
	RevisionID   string          `json:"revision_id"`
	CanonicalURL string          `json:"canonical_url"`
	PublishedAt  *time.Time      `json:"published_at"`
	UpdatedAt    *time.Time      `json:"updated_at,omitempty"`
	Headline     string          `json:"headline"`
	BodyText     string          `json:"body_text"`
	RawPayload   json.RawMessage `json:"raw_payload"`
}

// FetchResult は、1回の記事取得で得られた記事と継続可能な警告を表します。
type FetchResult struct {
	Articles []Article `json:"articles"`
	Warnings []string  `json:"warnings"`
	Skipped  int       `json:"skipped"`
}
