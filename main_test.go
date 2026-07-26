package main

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

/*
TestSplitCommandUsesServeByDefault は、コマンド省略時にserveが選ばれることを検証します。

機能:
  - 先頭に明示コマンドがない引数をserveコマンドと元のフラグへ分割できることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestSplitCommandUsesServeByDefault(t *testing.T) {
	t.Parallel()

	command, args := splitCommand([]string{"-conf", "settings"})
	if command != "serve" {
		t.Errorf("command = %q, 期待値は serve", command)
	}
	if len(args) != 2 || args[0] != "-conf" || args[1] != "settings" {
		t.Errorf("args = %#v, 元のフラグを期待", args)
	}
}

// ----------------------------------------

/*
TestParseOptionsRequiresFetchSourceID は、fetchの取得対象IDが必須であることを検証します。

機能:
  - fetchコマンドでsource-idの不足を拒否し、有効な取得対象IDと設定先を解析できることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestParseOptionsRequiresFetchSourceID(t *testing.T) {
	t.Parallel()

	if _, err := parseOptions("fetch", []string{"-conf", "conf"}); err == nil {
		t.Fatal("parseOptions() error = nil, source-id不足エラーを期待")
	}
	options, err := parseOptions(
		"fetch",
		[]string{"-conf", "conf", "-source-id", "42"},
	)
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if options.sourceID != 42 || options.configDirectory != "conf" {
		t.Errorf("options = %+v, sourceID=42 conf=confを期待", options)
	}
}

// ----------------------------------------

/*
TestWaitForScheduler は、完了通知と期限超過の両方を検証します。

機能:
  - スケジューラー完了時の正常終了とコンテキスト期限超過時のエラー伝播を確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestWaitForScheduler(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	close(done)
	if err := waitForScheduler(context.Background(), done); err != nil {
		t.Errorf("完了済み waitForScheduler() error = %v", err)
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := waitForScheduler(timeoutCtx, make(chan struct{})); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Errorf("期限超過 error = %v, DeadlineExceededを期待", err)
	}
}

// ----------------------------------------

/*
TestValidateRedirectURL は、外部HTTPリダイレクトURLの基本制約を検証します。

機能:
  - 公開HTTPS URLを許可し、HTTP以外のfile URLを拒否することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestValidateRedirectURL(t *testing.T) {
	t.Parallel()

	valid, err := url.Parse("https://example.com/news")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if err := validateRedirectURL(valid); err != nil {
		t.Errorf("公開HTTPS URL error = %v", err)
	}

	invalid, err := url.Parse("file:///etc/passwd")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if err := validateRedirectURL(invalid); err == nil {
		t.Error("file URL error = nil, 拒否エラーを期待")
	}
}
