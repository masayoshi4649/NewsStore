package domain

import (
	"encoding/json"
	"time"
)

// SourceKind は、記事取得元のコネクタ種別を表します。
type SourceKind string

const (
	// SourceKindRSS は、RSSまたはAtomフィードから取得するコネクタ種別を表します。
	SourceKindRSS SourceKind = "rss"
	// SourceKindReuters は、Reuters APIから取得するコネクタ種別を表します。
	SourceKindReuters SourceKind = "reuters"
)

// Source は、記事取得元とその実行設定を表します。
type Source struct {
	ID                   int64           `json:"id"`
	Name                 string          `json:"name"`
	Kind                 SourceKind      `json:"kind"`
	EndpointURL          string          `json:"endpoint_url"`
	Enabled              bool            `json:"enabled"`
	FetchIntervalMinutes int             `json:"fetch_interval_minutes"`
	MaxItems             int             `json:"max_items"`
	Settings             json.RawMessage `json:"settings"`
	LastFetchedAt        *time.Time      `json:"last_fetched_at,omitempty"`
	NextFetchAt          *time.Time      `json:"next_fetch_at,omitempty"`
	LastError            string          `json:"last_error,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}
