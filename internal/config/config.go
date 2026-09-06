// Package config は、NewsStore の設定ファイルを読み込み、検証する機能を提供します。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultSystemPort          = 8080
	defaultPostgresPort        = 5432
	defaultPostgresSSLMode     = "require"
	defaultPostgresMaxOpen     = 25
	defaultPostgresMaxIdle     = 5
	defaultWorkerPollInterval  = time.Minute
	defaultWorkerConcurrency   = 2
	defaultHTTPTimeout         = 20 * time.Second
	defaultHTTPMaxResponseSize = int64(8 * 1024 * 1024)
	defaultHTTPUserAgent       = "NewsStore/0.1"
	defaultMCPArticleLimit     = 20
	maxPostgresConnections     = 1000
	minWorkerPollInterval      = time.Second
	maxWorkerPollInterval      = 24 * time.Hour
	maxWorkerConcurrency       = 8
	minHTTPTimeout             = time.Second
	maxHTTPTimeout             = 5 * time.Minute
	maxHTTPResponseSize        = int64(64 * 1024 * 1024)
	maxMCPArticleLimit         = 100
)

// Duration は、TOML の文字列で指定された期間を保持します。
type Duration struct {
	time.Duration
}

// Config は、NewsStore 全体の設定を保持します。
type Config struct {
	System    SystemConfig    `toml:"SYSTEM"`
	Postgres  PostgresConfig  `toml:"postgres"`
	Worker    WorkerConfig    `toml:"worker"`
	Retention RetentionConfig `toml:"retention"`
	HTTP      HTTPConfig      `toml:"http"`
	MCP       MCPConfig       `toml:"MCP"`
}

// SystemConfig は、NewsStore 全体のシステム設定を保持します。
type SystemConfig struct {
	Port int `toml:"Port"`
}

// PostgresConfig は、PostgreSQL への接続とマイグレーションの設定を保持します。
type PostgresConfig struct {
	Host         string `toml:"host"`
	Port         int    `toml:"port"`
	Username     string `toml:"username"`
	Password     string `toml:"password"`
	DBName       string `toml:"dbname"`
	SSLMode      string `toml:"sslmode"`
	MaxOpenConns int    `toml:"max_open_conns"`
	MaxIdleConns int    `toml:"max_idle_conns"`
	AutoMigrate  bool   `toml:"auto_migrate"`
}

// WorkerConfig は、記事取得ワーカーの実行間隔と並列数を保持します。
type WorkerConfig struct {
	PollInterval Duration `toml:"poll_interval"`
	Concurrency  int      `toml:"concurrency"`
}

// RetentionConfig は、記事の保持期間制限を適用するかどうかを保持します。
type RetentionConfig struct {
	Enabled bool `toml:"enabled"`
}

// HTTPConfig は、外部 HTTP 通信の制限と識別情報を保持します。
type HTTPConfig struct {
	Timeout              Duration `toml:"timeout"`
	MaxResponseBytes     int64    `toml:"max_response_bytes"`
	UserAgent            string   `toml:"user_agent"`
	AllowPrivateNetworks bool     `toml:"allow_private_networks"`
}

// MCPConfig は、読み取り専用 MCP の記事参照設定を保持します。
type MCPConfig struct {
	DefaultArticleLimit int `toml:"DefaultArticleLimit"`
}

/*
Default は、アプリケーション設定の既定値を生成します。

機能:
  - サーバー、PostgreSQL、ワーカー、外部HTTP、MCPの安全な既定値を組み立てる

引数:
  - なし

返り値:
  - Config: 既定値を格納した設定
*/
func Default() Config {
	return Config{
		System: SystemConfig{
			Port: defaultSystemPort,
		},
		Postgres: PostgresConfig{
			Port:         defaultPostgresPort,
			SSLMode:      defaultPostgresSSLMode,
			MaxOpenConns: defaultPostgresMaxOpen,
			MaxIdleConns: defaultPostgresMaxIdle,
			AutoMigrate:  true,
		},
		Worker: WorkerConfig{
			PollInterval: Duration{Duration: defaultWorkerPollInterval},
			Concurrency:  defaultWorkerConcurrency,
		},
		HTTP: HTTPConfig{
			Timeout:          Duration{Duration: defaultHTTPTimeout},
			MaxResponseBytes: defaultHTTPMaxResponseSize,
			UserAgent:        defaultHTTPUserAgent,
		},
		MCP: MCPConfig{
			DefaultArticleLimit: defaultMCPArticleLimit,
		},
	}
}

// ----------------------------------------

/*
LoadDir は、指定ディレクトリ直下の TOML ファイルをファイル名昇順で読み込みます。

各ファイルは同じ設定へ順次デコードされるため、後のファイルで指定された項目だけが
前の設定を上書きします。読み込み後は設定値の検証も行います。

機能:
  - 既定値へ複数のTOMLファイルを名前順で上書きする
  - 未知項目と統合後の不正値をエラーにする

引数:
  - dir string: TOML 設定ファイルを格納したディレクトリのパス

返り値:
  - Config: 既定値と全 TOML ファイルを統合した設定
  - error: ディレクトリの読み込み、TOML の解析、未知項目、または検証に失敗した場合のエラー
*/
func LoadDir(dir string) (Config, error) {
	if strings.TrimSpace(dir) == "" {
		return Config{}, errors.New("設定ディレクトリが指定されていません")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return Config{}, fmt.Errorf("設定ディレクトリ %q を読み込めません: %w", dir, err)
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return Config{}, fmt.Errorf("設定ディレクトリ %q に TOML ファイルがありません", dir)
	}

	cfg := Default()
	for _, path := range paths {
		metadata, decodeErr := toml.DecodeFile(path, &cfg)
		if decodeErr != nil {
			return Config{}, fmt.Errorf("設定ファイル %q を解析できません: %w", path, decodeErr)
		}
		if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, 0, len(undecoded))
			for _, key := range undecoded {
				keys = append(keys, key.String())
			}
			return Config{}, fmt.Errorf(
				"設定ファイル %q に未知の項目があります: %s",
				path,
				strings.Join(keys, ", "),
			)
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("設定値が不正です: %w", err)
	}
	return cfg, nil
}

// ----------------------------------------

/*
Validate は、アプリケーション設定が実行可能な値であることを検証します。

機能:
  - 待受、DB、ワーカー、外部HTTP、MCPの範囲と相関条件をまとめて検証する
  - 検出した複数の不正値を結合して返す

引数:
  - なし

返り値:
  - error: 設定値が不正な場合のエラー。すべて有効な場合は nil
*/
func (c Config) Validate() error {
	var validationErrors []error

	if c.System.Port < 1 || c.System.Port > 65535 {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("SYSTEM.Port は 1 以上 65535 以下である必要があります: %d", c.System.Port),
		)
	}

	// ----------------------------------------

	if strings.TrimSpace(c.Postgres.Host) == "" {
		validationErrors = append(validationErrors, errors.New("postgres.host は空にできません"))
	}
	if c.Postgres.Port < 1 || c.Postgres.Port > 65535 {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("postgres.port は 1 以上 65535 以下である必要があります: %d", c.Postgres.Port),
		)
	}
	if strings.TrimSpace(c.Postgres.Username) == "" {
		validationErrors = append(validationErrors, errors.New("postgres.username は空にできません"))
	}
	if strings.TrimSpace(c.Postgres.DBName) == "" {
		validationErrors = append(validationErrors, errors.New("postgres.dbname は空にできません"))
	}
	if strings.TrimSpace(c.Postgres.SSLMode) == "" {
		validationErrors = append(validationErrors, errors.New("postgres.sslmode は空にできません"))
	} else if !validPostgresSSLMode(c.Postgres.SSLMode) {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("postgres.sslmode が未対応です: %q", c.Postgres.SSLMode),
		)
	}
	if c.Postgres.MaxOpenConns < 1 {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("postgres.max_open_conns は 1 以上である必要があります: %d", c.Postgres.MaxOpenConns),
		)
	} else if c.Postgres.MaxOpenConns > maxPostgresConnections {
		validationErrors = append(
			validationErrors,
			fmt.Errorf(
				"postgres.max_open_conns は %d 以下である必要があります: %d",
				maxPostgresConnections,
				c.Postgres.MaxOpenConns,
			),
		)
	}
	if c.Postgres.MaxIdleConns < 0 {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("postgres.max_idle_conns は 0 以上である必要があります: %d", c.Postgres.MaxIdleConns),
		)
	}
	if c.Postgres.MaxIdleConns > c.Postgres.MaxOpenConns {
		validationErrors = append(
			validationErrors,
			errors.New("postgres.max_idle_conns は postgres.max_open_conns 以下である必要があります"),
		)
	}

	// ----------------------------------------

	if c.Worker.PollInterval.Duration < minWorkerPollInterval ||
		c.Worker.PollInterval.Duration > maxWorkerPollInterval {
		validationErrors = append(
			validationErrors,
			fmt.Errorf(
				"worker.poll_interval は %s 以上 %s 以下である必要があります",
				minWorkerPollInterval,
				maxWorkerPollInterval,
			),
		)
	}
	if c.Worker.Concurrency < 1 {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("worker.concurrency は 1 以上である必要があります: %d", c.Worker.Concurrency),
		)
	} else if c.Worker.Concurrency > maxWorkerConcurrency {
		validationErrors = append(
			validationErrors,
			fmt.Errorf(
				"worker.concurrency は %d 以下である必要があります: %d",
				maxWorkerConcurrency,
				c.Worker.Concurrency,
			),
		)
	}
	if c.Worker.Concurrency > 0 &&
		c.Postgres.MaxOpenConns < c.Worker.Concurrency*2 {
		validationErrors = append(
			validationErrors,
			fmt.Errorf(
				"postgres.max_open_conns は worker.concurrency の2倍以上が必要です: max_open_conns=%d concurrency=%d",
				c.Postgres.MaxOpenConns,
				c.Worker.Concurrency,
			),
		)
	}

	// ----------------------------------------

	if c.HTTP.Timeout.Duration < minHTTPTimeout ||
		c.HTTP.Timeout.Duration > maxHTTPTimeout {
		validationErrors = append(
			validationErrors,
			fmt.Errorf(
				"http.timeout は %s 以上 %s 以下である必要があります",
				minHTTPTimeout,
				maxHTTPTimeout,
			),
		)
	}
	if c.HTTP.MaxResponseBytes < 1 {
		validationErrors = append(
			validationErrors,
			fmt.Errorf("http.max_response_bytes は 1 以上である必要があります: %d", c.HTTP.MaxResponseBytes),
		)
	} else if c.HTTP.MaxResponseBytes > maxHTTPResponseSize {
		validationErrors = append(
			validationErrors,
			fmt.Errorf(
				"http.max_response_bytes は %d 以下である必要があります: %d",
				maxHTTPResponseSize,
				c.HTTP.MaxResponseBytes,
			),
		)
	}
	if strings.TrimSpace(c.HTTP.UserAgent) == "" {
		validationErrors = append(validationErrors, errors.New("http.user_agent は空にできません"))
	}

	// ----------------------------------------

	if c.MCP.DefaultArticleLimit < 1 || c.MCP.DefaultArticleLimit > maxMCPArticleLimit {
		validationErrors = append(
			validationErrors,
			fmt.Errorf(
				"MCP.DefaultArticleLimit は 1 以上 %d 以下である必要があります: %d",
				maxMCPArticleLimit,
				c.MCP.DefaultArticleLimit,
			),
		)
	}

	return errors.Join(validationErrors...)
}

// ----------------------------------------

/*
validPostgresSSLMode は、pgx が利用できる PostgreSQL SSL モードか判定します。

機能:
  - PostgreSQL 接続で一般的な 6 種類の SSL モードだけを許可する

引数:
  - value string: postgres.sslmode の設定値

返り値:
  - bool: 対応する SSL モードの場合は true
*/
func validPostgresSSLMode(value string) bool {
	switch strings.TrimSpace(value) {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

/*
UnmarshalText は、TOML の期間文字列を time.Duration として解析します。

機能:
  - Goの期間表記を解析してDurationへ設定する

引数:
  - text []byte: 「30s」や「5m」のような期間文字列

返り値:
  - error: 期間文字列を解析できない場合のエラー。解析に成功した場合は nil
*/
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("期間 %q を解析できません: %w", string(text), err)
	}
	d.Duration = parsed
	return nil
}

// ----------------------------------------

/*
String は、保持している期間を文字列へ変換します。

機能:
  - 内部のtime.Durationを標準の期間表記へ変換する

引数:
  - なし

返り値:
  - string: time.Duration 形式の期間文字列
*/
func (d Duration) String() string {
	return d.Duration.String()
}
