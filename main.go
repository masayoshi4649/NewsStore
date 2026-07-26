package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/config"
	"github.com/masayoshi4649/NewsStore/internal/connector"
	"github.com/masayoshi4649/NewsStore/internal/connector/reuters"
	"github.com/masayoshi4649/NewsStore/internal/connector/rss"
	"github.com/masayoshi4649/NewsStore/internal/ingest"
	"github.com/masayoshi4649/NewsStore/internal/netpolicy"
	"github.com/masayoshi4649/NewsStore/internal/scheduler"
	"github.com/masayoshi4649/NewsStore/internal/store/postgres"
	"github.com/masayoshi4649/NewsStore/internal/web"
)

const (
	databaseStartupTimeout   = 15 * time.Second
	databaseMigrationTimeout = 10 * time.Minute
	databaseRecoveryTimeout  = time.Minute
	serverShutdownTimeout    = 15 * time.Second
)

/*
main は、NewsStore コマンドを実行して終了コードへ反映します。

機能:
  - コマンドライン引数を run へ渡す
  - 失敗時は安全なエラーを標準エラーへ表示して終了コード 1 とする

引数:
  - なし

返り値:
  - なし
*/
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "エラー: %v\n", err)
		os.Exit(1)
	}
}

// ----------------------------------------

/*
run は、serve、migrate、fetch の各コマンドを振り分けます。

機能:
  - コマンドと共通設定ディレクトリを解析する
  - TOML 設定を読み込んで PostgreSQL へ接続する
  - 必要なコネクタとサービスを組み立てて指定処理を実行する

引数:
  - args []string: 実行ファイル名を除いたコマンドライン引数

返り値:
  - error: 引数、設定、DB 接続、または各コマンド処理に失敗した場合のエラー
*/
func run(args []string) error {
	command, commandArgs := splitCommand(args)
	options, err := parseOptions(command, commandArgs)
	if err != nil {
		return err
	}

	cfg, err := config.LoadDir(options.configDirectory)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	startupCtx, cancel := context.WithTimeout(context.Background(), databaseStartupTimeout)
	store, err := postgres.Open(startupCtx, cfg.Postgres)
	cancel()
	if err != nil {
		return err
	}
	defer store.Close()

	// ----------------------------------------

	if command == "migrate" || cfg.Postgres.AutoMigrate {
		migrationCtx, migrationCancel := context.WithTimeout(
			context.Background(),
			databaseMigrationTimeout,
		)
		migrationErr := store.Migrate(migrationCtx)
		migrationCancel()
		if migrationErr != nil {
			return migrationErr
		}
	}
	if command == "migrate" {
		fmt.Println("データベースマイグレーションが完了しました")
		return nil
	}

	// ----------------------------------------

	recoveryCtx, recoveryCancel := context.WithTimeout(
		context.Background(),
		databaseRecoveryTimeout,
	)
	recoveredRuns, err := store.RecoverStaleRuns(recoveryCtx, 2*time.Hour)
	recoveryCancel()
	if err != nil {
		return err
	}
	if recoveredRuns > 0 {
		logger.Warn("異常終了後の取得履歴を回復しました", "runs", recoveredRuns)
	}

	httpClient := newHTTPClient(
		cfg.HTTP.Timeout.Duration,
		cfg.HTTP.AllowPrivateNetworks,
	)
	reutersConnector := reuters.New(
		httpClient,
		cfg.HTTP.UserAgent,
		cfg.HTTP.MaxResponseBytes,
	)
	reutersConnector.SetRevisionChecker(store)
	registry := connector.NewRegistry(
		rss.New(httpClient, cfg.HTTP.UserAgent, cfg.HTTP.MaxResponseBytes),
		reutersConnector,
	)
	ingestService := ingest.New(store, registry, logger)

	if command == "fetch" {
		fetchCtx, stopFetch := signal.NotifyContext(
			context.Background(),
			os.Interrupt,
			syscall.SIGTERM,
		)
		defer stopFetch()
		summary, err := ingestService.Run(fetchCtx, options.sourceID, "cli")
		if err != nil {
			return err
		}
		fmt.Printf(
			"取得が完了しました: 状態=%s 取得=%d 新規=%d 重複=%d 警告=%d\n",
			summary.Status,
			summary.FetchedCount,
			summary.InsertedCount,
			summary.SkippedCount,
			summary.WarningCount,
		)
		return nil
	}

	return serve(cfg, store, ingestService, logger)
}

// ----------------------------------------

// commandOptions は、各コマンドから共通化した実行オプションです。
type commandOptions struct {
	configDirectory string
	sourceID        int64
}

/*
splitCommand は、先頭引数から実行コマンドと残りの引数を分離します。

機能:
  - 引数なしまたは先頭がフラグの場合は serve を既定コマンドとする

引数:
  - args []string: 実行ファイル名を除いたコマンドライン引数

返り値:
  - string: serve、migrate、fetch のいずれか
  - []string: コマンド固有フラグとして解析する残りの引数
*/
func splitCommand(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "serve", args
	}
	return args[0], args[1:]
}

// ----------------------------------------

/*
parseOptions は、指定コマンドのフラグを解析して検証します。

機能:
  - 全コマンドの conf ディレクトリを解析する
  - fetch コマンドでは取得対象 ID を必須とする
  - 未対応コマンドを利用方法付きエラーにする

引数:
  - command string: 実行するコマンド名
  - args []string: コマンド固有フラグ

返り値:
  - commandOptions: 検証済みの実行オプション
  - error: 未対応コマンド、フラグ解析、または必須値不足のエラー
*/
func parseOptions(command string, args []string) (commandOptions, error) {
	if command != "serve" && command != "migrate" && command != "fetch" {
		return commandOptions{}, fmt.Errorf(
			"未対応のコマンドです: %q\n利用方法: newsstore [serve|migrate|fetch] [-conf conf] [-source-id ID]",
			command,
		)
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configDirectory := flags.String("conf", "conf", "TOML 設定ディレクトリ")
	sourceID := flags.Int64("source-id", 0, "fetch で取得する対象 ID")
	if err := flags.Parse(args); err != nil {
		return commandOptions{}, fmt.Errorf("コマンドライン引数が不正です: %w", err)
	}
	if flags.NArg() > 0 {
		return commandOptions{}, fmt.Errorf("解釈できない引数があります: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*configDirectory) == "" {
		return commandOptions{}, errors.New("設定ディレクトリは空にできません")
	}
	if command == "fetch" && *sourceID < 1 {
		return commandOptions{}, errors.New("fetch には -source-id で 1 以上の取得対象 ID が必要です")
	}
	return commandOptions{
		configDirectory: *configDirectory,
		sourceID:        *sourceID,
	}, nil
}

// ----------------------------------------

/*
serve は、定期取得ワーカーと Gin 管理画面を起動します。

機能:
  - OS シグナルで終了するルートコンテキストを作る
  - スケジューラーと HTTP サーバーを並行起動する
  - 終了時に HTTP サーバーを猶予付きで停止する

引数:
  - cfg config.Config: サーバーとワーカーの設定
  - store *postgres.Store: 管理画面とワーカーが共有する Repository
  - ingestService *ingest.Service: 記事取得を実行するサービス
  - logger *slog.Logger: 起動・停止・異常を記録するロガー

返り値:
  - error: 管理画面初期化または HTTP サーバー異常終了のエラー
*/
func serve(
	cfg config.Config,
	store *postgres.Store,
	ingestService *ingest.Service,
	logger *slog.Logger,
) error {
	rootCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	manager := scheduler.New(
		store,
		ingestService,
		cfg.Worker.PollInterval.Duration,
		cfg.Worker.Concurrency,
		logger,
	)
	webServer, err := web.New(store, manager, logger)
	if err != nil {
		return err
	}

	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		manager.Run(rootCtx, cfg.Worker.Concurrency)
	}()

	listenAddress := fmt.Sprintf(":%d", cfg.System.Port)
	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           webServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("管理画面を起動しました", "address", listenAddress)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-rootCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		serverErr := httpServer.Shutdown(shutdownCtx)
		schedulerErr := waitForScheduler(shutdownCtx, schedulerDone)
		if err := errors.Join(serverErr, schedulerErr); err != nil {
			return fmt.Errorf("NewsStore を安全に停止できません: %w", err)
		}
		logger.Info("NewsStore を停止しました")
		return nil
	case err := <-serverErrors:
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		schedulerErr := waitForScheduler(shutdownCtx, schedulerDone)
		if errors.Is(err, http.ErrServerClosed) {
			return schedulerErr
		}
		return errors.Join(
			fmt.Errorf("管理画面が異常終了しました: %w", err),
			schedulerErr,
		)
	}
}

// ----------------------------------------

/*
waitForScheduler は、DB 接続を閉じる前に全取得ワーカーの終了を待ちます。

機能:
  - スケジューラーの完了通知または停止期限の到来を待つ
  - 期限超過時は安全停止できなかったことをエラーにする

引数:
  - ctx context.Context: 安全停止の期限を持つコンテキスト
  - done <-chan struct{}: スケジューラー完了時に閉じられるチャネル

返り値:
  - error: 期限前に完了しなかった場合のエラー。正常終了時は nil
*/
func waitForScheduler(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("記事取得ワーカーの停止が期限を超えました: %w", ctx.Err())
	}
}

// ----------------------------------------

/*
newHTTPClient は、外部取得向けの制限付き HTTP クライアントを生成します。

機能:
  - リクエスト全体のタイムアウトを設定する
  - リダイレクト回数、URL スキーム、ユーザー情報を検証する

引数:
  - timeout time.Duration: 一回の外部 HTTP リクエスト全体の期限
  - allowPrivateNetworks bool: 内部ネットワーク宛の取得を明示的に許可する設定

返り値:
  - *http.Client: RSS と Reuters が共有する HTTP クライアント
*/
func newHTTPClient(timeout time.Duration, allowPrivateNetworks bool) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: netpolicy.NewTransport(allowPrivateNetworks),
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("リダイレクト回数が上限を超えました")
			}
			return validateRedirectURL(request.URL)
		},
	}
}

// ----------------------------------------

/*
validateRedirectURL は、外部 HTTP のリダイレクト先 URL を検証します。

機能:
  - http または https だけを許可する
  - ホストなしと URL 内認証情報を拒否する

引数:
  - target *url.URL: HTTP クライアントが遷移しようとする URL

返り値:
  - error: リダイレクト先が安全要件を満たさない場合のエラー
*/
func validateRedirectURL(target *url.URL) error {
	if target == nil {
		return errors.New("リダイレクト先 URL がありません")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("リダイレクト先の URL スキームを許可していません: %q", target.Scheme)
	}
	if target.Hostname() == "" {
		return errors.New("リダイレクト先 URL にホストがありません")
	}
	if target.User != nil {
		return errors.New("リダイレクト先 URL にユーザー情報は指定できません")
	}
	if port := target.Port(); port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return errors.New("リダイレクト先 URL のポートが不正です")
		}
	}
	return nil
}
