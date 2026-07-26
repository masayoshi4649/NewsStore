package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/*
TestDefault は、Default がアプリケーションの既定値を返すことを検証します。

機能:
  - システム、PostgreSQL、ワーカー、HTTP、MCPの主要な既定設定値を確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestDefault(t *testing.T) {
	t.Parallel()

	cfg := Default()

	if cfg.System.Port != 8080 {
		t.Errorf("System.Port = %d, 期待値は %d", cfg.System.Port, 8080)
	}
	if cfg.Postgres.Port != 5432 {
		t.Errorf("Postgres.Port = %d, 期待値は %d", cfg.Postgres.Port, 5432)
	}
	if cfg.Postgres.SSLMode != "require" {
		t.Errorf("Postgres.SSLMode = %q, 期待値は %q", cfg.Postgres.SSLMode, "require")
	}
	if cfg.Postgres.MaxOpenConns != 25 {
		t.Errorf("Postgres.MaxOpenConns = %d, 期待値は %d", cfg.Postgres.MaxOpenConns, 25)
	}
	if cfg.Postgres.MaxIdleConns != 5 {
		t.Errorf("Postgres.MaxIdleConns = %d, 期待値は %d", cfg.Postgres.MaxIdleConns, 5)
	}
	if !cfg.Postgres.AutoMigrate {
		t.Error("Postgres.AutoMigrate = false, 期待値は true")
	}
	if cfg.Worker.PollInterval.Duration != time.Minute {
		t.Errorf("Worker.PollInterval = %s, 期待値は %s", cfg.Worker.PollInterval, time.Minute)
	}
	if cfg.Worker.Concurrency != 2 {
		t.Errorf("Worker.Concurrency = %d, 期待値は %d", cfg.Worker.Concurrency, 2)
	}
	if cfg.HTTP.Timeout.Duration != 20*time.Second {
		t.Errorf("HTTP.Timeout = %s, 期待値は %s", cfg.HTTP.Timeout, 20*time.Second)
	}
	if cfg.HTTP.MaxResponseBytes != 8*1024*1024 {
		t.Errorf("HTTP.MaxResponseBytes = %d, 期待値は %d", cfg.HTTP.MaxResponseBytes, 8*1024*1024)
	}
	if cfg.HTTP.UserAgent != "NewsStore/0.1" {
		t.Errorf("HTTP.UserAgent = %q, 期待値は %q", cfg.HTTP.UserAgent, "NewsStore/0.1")
	}
	if cfg.MCP.DefaultArticleLimit != 20 {
		t.Errorf(
			"MCP.DefaultArticleLimit = %d, 期待値は %d",
			cfg.MCP.DefaultArticleLimit,
			20,
		)
	}
}

// ----------------------------------------

/*
TestLoadDir は、複数の TOML ファイルがファイル名順に統合されることを検証します。

機能:
  - 基本設定と上書き設定を読み込み、後順位ファイルの値と未指定の既定値を確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLoadDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestFile(t, dir, "20-override.toml", `
[SYSTEM]
Port = 9090

[postgres]
host = "db-override"

[worker]
poll_interval = "15s"

[http]
timeout = "3s"
`)
	writeTestFile(t, dir, "10-base.toml", `
[postgres]
host = "db-base"
username = "news"
password = "password"
dbname = "news_store"

[worker]
concurrency = 7

[http]
max_response_bytes = 4096
`)
	writeTestFile(t, dir, "15-mcp.toml", `
[MCP]
DefaultArticleLimit = 40
`)

	cfg, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}

	if cfg.System.Port != 9090 {
		t.Errorf("System.Port = %d, 期待値は %d", cfg.System.Port, 9090)
	}
	if cfg.Postgres.Host != "db-override" {
		t.Errorf("Postgres.Host = %q, 期待値は %q", cfg.Postgres.Host, "db-override")
	}
	if cfg.Postgres.Username != "news" || cfg.Postgres.DBName != "news_store" {
		t.Errorf(
			"PostgreSQL 情報 = (%q, %q), 期待値は (%q, %q)",
			cfg.Postgres.Username,
			cfg.Postgres.DBName,
			"news",
			"news_store",
		)
	}
	if cfg.Postgres.Port != 5432 {
		t.Errorf("Postgres.Port = %d, 期待値は既定値 %d", cfg.Postgres.Port, 5432)
	}
	if cfg.Worker.PollInterval.Duration != 15*time.Second {
		t.Errorf("Worker.PollInterval = %s, 期待値は %s", cfg.Worker.PollInterval, 15*time.Second)
	}
	if cfg.Worker.Concurrency != 7 {
		t.Errorf("Worker.Concurrency = %d, 期待値は %d", cfg.Worker.Concurrency, 7)
	}
	if cfg.HTTP.Timeout.Duration != 3*time.Second {
		t.Errorf("HTTP.Timeout = %s, 期待値は %s", cfg.HTTP.Timeout, 3*time.Second)
	}
	if cfg.HTTP.MaxResponseBytes != 4096 {
		t.Errorf("HTTP.MaxResponseBytes = %d, 期待値は %d", cfg.HTTP.MaxResponseBytes, 4096)
	}
	if cfg.MCP.DefaultArticleLimit != 40 {
		t.Errorf(
			"MCP.DefaultArticleLimit = %d, 期待値は %d",
			cfg.MCP.DefaultArticleLimit,
			40,
		)
	}
}

// ----------------------------------------

/*
TestLoadDirUnknownKey は、未知の TOML 項目がエラーになることを検証します。

機能:
  - 未定義の設定項目を含むTOMLを拒否し、エラーへ項目名を含めることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLoadDirUnknownKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeValidMinimumConfig(t, dir)
	writeTestFile(t, dir, "20-unknown.toml", `
[postgres]
unknown_value = true
`)

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("LoadDir() error = nil, 未知項目のエラーを期待")
	}
	if !strings.Contains(err.Error(), "postgres.unknown_value") {
		t.Errorf("LoadDir() error = %q, 未知項目名を含むエラーを期待", err)
	}
}

// ----------------------------------------

/*
TestLoadDirNoTOML は、対象 TOML ファイルがない場合にエラーになることを検証します。

機能:
  - TOML以外のファイルしかない設定ディレクトリを読み込みエラーにすることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLoadDirNoTOML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeTestFile(t, dir, "README.txt", "設定ファイルではありません")

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("LoadDir() error = nil, TOML ファイルなしのエラーを期待")
	}
}

// ----------------------------------------

/*
TestLoadDirInvalidDuration は、不正な期間文字列が解析エラーになることを検証します。

機能:
  - ワーカー間隔へ期間として解析できない文字列を指定した設定を拒否することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestLoadDirInvalidDuration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeValidMinimumConfig(t, dir)
	writeTestFile(t, dir, "20-invalid.toml", `
[worker]
poll_interval = "毎分"
`)

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("LoadDir() error = nil, 期間文字列の解析エラーを期待")
	}
}

// ----------------------------------------

/*
TestValidate は、主要な不正値がまとめて検証エラーになることを検証します。

機能:
  - 複数区分の不正設定を一括検証し、各設定名を含むエラーへ集約することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestValidate(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.System.Port = 0
	cfg.Postgres.Port = 70000
	cfg.Postgres.MaxIdleConns = cfg.Postgres.MaxOpenConns + 1
	cfg.Worker.Concurrency = 0
	cfg.HTTP.Timeout = Duration{}
	cfg.MCP.DefaultArticleLimit = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, 検証エラーを期待")
	}

	for _, expected := range []string{
		"SYSTEM.Port",
		"postgres.host",
		"postgres.port",
		"postgres.username",
		"postgres.dbname",
		"postgres.max_idle_conns",
		"worker.concurrency",
		"http.timeout",
		"MCP.DefaultArticleLimit",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("Validate() error = %q, %q を含むことを期待", err, expected)
		}
	}
}

// ----------------------------------------

/*
TestValidateAllowsClosedNetworkPortWithoutAuthentication は、閉域ネットワーク向けポートを認証設定なしで許可することを検証します。

機能:
  - SYSTEM.Portだけで全インターフェース待受を構成できることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestValidateAllowsClosedNetworkPortWithoutAuthentication(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Postgres.Host = "db"
	cfg.Postgres.Username = "news"
	cfg.Postgres.DBName = "newsstore"
	cfg.System.Port = 8080

	if err := cfg.Validate(); err != nil {
		t.Errorf("認証なし閉域待受の Validate() error = %v", err)
	}
}

// ----------------------------------------

/*
TestValidateRejectsResourceLimitOverflow は、過大な接続数・並列数・HTTP制限を拒否することを検証します。

機能:
  - DB接続数、ワーカー設定、HTTP設定が各上限を超えた場合の検証エラーを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestValidateRejectsResourceLimitOverflow(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Postgres.Host = "db"
	cfg.Postgres.Username = "news"
	cfg.Postgres.DBName = "newsstore"
	cfg.Postgres.MaxOpenConns = maxPostgresConnections + 1
	cfg.Worker.Concurrency = maxWorkerConcurrency + 1
	cfg.Worker.PollInterval = Duration{Duration: maxWorkerPollInterval + time.Second}
	cfg.HTTP.Timeout = Duration{Duration: maxHTTPTimeout + time.Second}
	cfg.HTTP.MaxResponseBytes = maxHTTPResponseSize + 1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, リソース上限エラーを期待")
	}
	for _, expected := range []string{
		"postgres.max_open_conns",
		"worker.concurrency",
		"worker.poll_interval",
		"http.timeout",
		"http.max_response_bytes",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("Validate() error = %q, %q を含むことを期待", err, expected)
		}
	}
}

// ----------------------------------------

/*
TestValidateRejectsMCPDefaultArticleLimitOverflow は、MCP の既定記事件数上限を検証します。

機能:
  - MCPの既定記事件数が許容範囲を超えた設定を拒否することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestValidateRejectsMCPDefaultArticleLimitOverflow(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Postgres.Host = "db"
	cfg.Postgres.Username = "news"
	cfg.Postgres.DBName = "newsstore"
	cfg.MCP.DefaultArticleLimit = maxMCPArticleLimit + 1

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "MCP.DefaultArticleLimit") {
		t.Fatalf("Validate() error = %v, MCP既定記事件数の上限エラーを期待", err)
	}
}

// ----------------------------------------

/*
TestValidateRequiresConnectionsForWorkerLocks は、ワーカーの専用ロック接続分を含むDB接続数を要求することを検証します。

機能:
  - DB最大接続数がワーカー並列数の2倍未満の場合に相関エラーとなることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestValidateRequiresConnectionsForWorkerLocks(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Postgres.Host = "db"
	cfg.Postgres.Username = "news"
	cfg.Postgres.DBName = "newsstore"
	cfg.Worker.Concurrency = 2
	cfg.Postgres.MaxOpenConns = 3
	cfg.Postgres.MaxIdleConns = 3

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "worker.concurrency の2倍以上") {
		t.Fatalf("Validate() error = %v, ワーカー用DB接続数の相関エラーを期待", err)
	}
}

// ----------------------------------------

/*
writeValidMinimumConfig は、検証を通過する最小限の PostgreSQL 設定を書き込みます。

機能:
  - PostgreSQL接続に必要な最小項目を持つ基本TOMLをテスト用ディレクトリへ作成する

引数:
  - t *testing.T: テスト状態を管理する値
  - dir string: 設定ファイルを書き込むディレクトリ

返り値:
  - なし
*/
func writeValidMinimumConfig(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, dir, "10-base.toml", `
[SYSTEM]
Port = 8080

[postgres]
host = "db"
username = "news"
dbname = "news_store"
`)
}

// ----------------------------------------

/*
writeTestFile は、テスト用ディレクトリへ指定内容のファイルを書き込みます。

機能:
  - 指定した名前と内容のテストファイルを所有者限定の権限で作成する

引数:
  - t *testing.T: テスト状態を管理する値
  - dir string: ファイルを書き込むディレクトリ
  - name string: 作成するファイル名
  - content string: ファイルへ書き込む内容

返り値:
  - なし
*/
func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("テストファイル %q を作成できません: %v", path, err)
	}
}
