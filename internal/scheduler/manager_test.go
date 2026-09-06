package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/ingest"
)

// fakeDueRepository は、期限確認と記事削除の動作をテスト用に設定する Repository を表します。
type fakeDueRepository struct {
	listDue       func(context.Context, int) ([]int64, error)
	deleteExpired func(context.Context) (int64, error)
}

// fakeRunner は、実行されたジョブをテストへ通知します。
type fakeRunner struct {
	called chan Job
}

/*
TestManagerDeduplicatesAndStops は、重複キュー抑止とキャンセル時の停止を検証します。

機能:
  - 同一取得対象の重複投入を抑止し、キャンセルで管理処理が終了することを確認します。

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestManagerDeduplicatesAndStops(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{called: make(chan Job, 1)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(fakeDueRepository{}, runner, time.Hour, 1, logger)

	added, err := manager.Enqueue(1, "manual")
	if err != nil || !added {
		t.Fatalf("最初の Enqueue() = (%t, %v), 追加成功を期待", added, err)
	}
	added, err = manager.Enqueue(1, "scheduled")
	if err != nil {
		t.Fatalf("重複 Enqueue() error = %v", err)
	}
	if added {
		t.Error("重複 Enqueue() added = true, 期待値は false")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Run(ctx, 1)
	}()

	select {
	case job := <-runner.called:
		if job.SourceID != 1 || job.TriggerType != "manual" {
			t.Errorf("実行ジョブ = %+v, 期待値は source_id=1 trigger=manual", job)
		}
	case <-time.After(time.Second):
		t.Fatal("キュー済みジョブが実行されません")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("キャンセル後に Manager.Run() が終了しません")
	}
}

// ----------------------------------------

/*
ListDueSourceIDs は、定期取得対象の期限確認を模擬します。

機能:
  - 指定された期限確認を実行し、未指定なら空の ID 一覧を返す

引数:
  - ctx context.Context: 期限確認に使用する DB 操作コンテキスト
  - limit int: 取得対象の最大件数

返り値:
  - []int64: テストで設定した取得対象 ID 一覧、未指定の場合は空の一覧
  - error: テストで設定した確認エラー、未指定の場合は nil
*/
func (r fakeDueRepository) ListDueSourceIDs(ctx context.Context, limit int) ([]int64, error) {
	if r.listDue != nil {
		return r.listDue(ctx, limit)
	}
	return []int64{}, nil
}

// ----------------------------------------

/*
DeleteExpiredArticles は、保持期限を過ぎた記事の削除を模擬します。

機能:
  - 指定された削除処理を実行し、未指定なら削除対象なしとして返す

引数:
  - ctx context.Context: 削除処理の期限とキャンセルを伝えるコンテキスト

返り値:
  - int64: テストで設定した削除件数、未指定の場合は 0
  - error: テストで設定した削除エラー、未指定の場合は nil
*/
func (r fakeDueRepository) DeleteExpiredArticles(ctx context.Context) (int64, error) {
	if r.deleteExpired != nil {
		return r.deleteExpired(ctx)
	}
	return 0, nil
}

// ----------------------------------------

/*
TestManagerDisablesArticleRetention は、自動削除が無効な場合の記事保持を検証します。

機能:
  - 既定の無効状態と明示的に無効へ戻した状態で起動時と定期実行時の削除を行わないことを確認する
  - 無効時には削除用のタイマーを作成しないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestManagerDisablesArticleRetention(t *testing.T) {
	t.Parallel()

	for _, explicitDisable := range []bool{false, true} {
		name := "既定で無効"
		if explicitDisable {
			name = "明示的に無効へ変更"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			checked := make(chan struct{}, 10)
			deleted := make(chan struct{}, 1)
			repository := fakeDueRepository{
				listDue: func(context.Context, int) ([]int64, error) {
					select {
					case checked <- struct{}{}:
					default:
					}
					return nil, nil
				},
				deleteExpired: func(context.Context) (int64, error) {
					select {
					case deleted <- struct{}{}:
					default:
					}
					return 0, nil
				},
			}
			manager := New(repository, &fakeRunner{}, time.Millisecond, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
			manager.retentionInterval = 0
			if explicitDisable {
				manager.SetArticleRetentionEnabled(true)
				manager.SetArticleRetentionEnabled(false)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				manager.Run(ctx, 1)
			}()

			for attempt := 0; attempt < 2; attempt++ {
				select {
				case <-checked:
				case <-time.After(time.Second):
					t.Fatal("自動削除が無効の状態で期限確認が実行されません")
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("キャンセル後にスケジューラーが終了しません")
			}
			select {
			case <-deleted:
				t.Error("自動削除が無効の状態で記事を削除しました")
			default:
			}
		})
	}
}

// ----------------------------------------

/*
TestManagerDeletesExpiredArticlesWithoutSources は、取得対象の状態に依存しない定期削除を検証します。

機能:
  - 取得対象がない場合と期限確認が失敗する場合の両方で起動直後から削除することを確認する
  - 削除に失敗しても次回の削除を実行し、通常の削除間隔が 1 時間であることを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestManagerDeletesExpiredArticlesWithoutSources(t *testing.T) {
	t.Parallel()

	for _, dueError := range []error{nil, errors.New("取得対象の期限確認に失敗")} {
		name := "取得対象なし"
		if dueError != nil {
			name = "期限確認失敗"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deleted := make(chan struct{}, 10)
			checked := make(chan struct{}, 1)
			repository := fakeDueRepository{
				listDue: func(context.Context, int) ([]int64, error) {
					select {
					case checked <- struct{}{}:
					default:
					}
					return nil, dueError
				},
				deleteExpired: func(context.Context) (int64, error) {
					select {
					case deleted <- struct{}{}:
					default:
					}
					return 0, errors.New("削除失敗後も継続することを確認")
				},
			}
			manager := New(repository, &fakeRunner{}, time.Hour, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if manager.retentionInterval != time.Hour {
				t.Fatalf("記事削除間隔 = %v, 期待値は 1 時間", manager.retentionInterval)
			}
			manager.retentionInterval = 10 * time.Millisecond
			manager.SetArticleRetentionEnabled(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				manager.Run(ctx, 1)
			}()

			for attempt := 0; attempt < 2; attempt++ {
				select {
				case <-deleted:
				case <-time.After(time.Second):
					t.Fatal("起動直後と次回の期限切れ記事削除が実行されません")
				}
			}
			select {
			case <-checked:
			case <-time.After(time.Second):
				t.Fatal("記事削除の失敗後に取得対象を確認していません")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("キャンセル後にスケジューラーが終了しません")
			}
		})
	}
}

// ----------------------------------------

/*
TestManagerDeletionDeadlineAndLogs は、削除の制限時間とログ出力を検証します。

機能:
  - 削除処理の期限が 1 分後に設定され、処理完了時にコンテキストが解放されることを確認する
  - 削除件数がある場合と失敗した場合にログを残し、対象なしの場合は残さないことを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestManagerDeletionDeadlineAndLogs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		deleted int64
		err     error
		log     string
	}{
		{name: "削除対象あり", deleted: 3, log: "deleted_count=3"},
		{name: "削除対象なし"},
		{name: "削除失敗", err: errors.New("記事削除に失敗"), log: "level=ERROR"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var buffer bytes.Buffer
			var cleanupCtx context.Context
			repository := fakeDueRepository{
				deleteExpired: func(ctx context.Context) (int64, error) {
					cleanupCtx = ctx
					deadline, ok := ctx.Deadline()
					remaining := time.Until(deadline)
					if !ok || remaining <= 59*time.Second || remaining > time.Minute {
						t.Errorf("削除の制限時間 = %v, 期待値は 1 分", remaining)
					}
					return test.deleted, test.err
				},
			}
			manager := New(repository, &fakeRunner{}, time.Hour, 1, slog.New(slog.NewTextHandler(&buffer, nil)))
			manager.SetArticleRetentionEnabled(true)
			manager.deleteExpiredArticles(context.Background())
			if cleanupCtx == nil || !errors.Is(cleanupCtx.Err(), context.Canceled) {
				t.Error("完了後に削除コンテキストが解放されていません")
			}
			if test.log == "" {
				if buffer.Len() != 0 {
					t.Errorf("削除対象なしでログが出力されました: %s", buffer.String())
				}
			} else if !strings.Contains(buffer.String(), test.log) {
				t.Errorf("ログ = %s, %q を含むことを期待", buffer.String(), test.log)
			}
		})
	}
}

// ----------------------------------------

/*
TestManagerCancelsRunningDeletion は、削除中の終了要求が伝達されることを検証します。

機能:
  - スケジューラーのキャンセルで実行中の削除を中止し、ワーカーも終了することを確認する

引数:
  - t *testing.T: テスト状態を管理する値

返り値:
  - なし
*/
func TestManagerCancelsRunningDeletion(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	repository := fakeDueRepository{
		deleteExpired: func(ctx context.Context) (int64, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}
	manager := New(repository, &fakeRunner{}, time.Hour, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	manager.SetArticleRetentionEnabled(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Run(ctx, 1)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("期限切れ記事の削除が開始しません")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("削除中のキャンセルでスケジューラーが終了しません")
	}
}

// ----------------------------------------

/*
Run は、実行された取得ジョブをテスト用チャネルへ通知します。

機能:
  - 受け取った取得対象 ID と起動方法をジョブとしてテストへ通知します。

引数:
  - ctx context.Context: 未使用の記事取得コンテキスト
  - sourceID int64: 実行する取得対象 ID
  - triggerType string: 実行の起動方法

返り値:
  - ingest.Summary: 空の正常終了結果
  - error: 常に nil
*/
func (r *fakeRunner) Run(
	_ context.Context,
	sourceID int64,
	triggerType string,
) (ingest.Summary, error) {
	r.called <- Job{SourceID: sourceID, TriggerType: triggerType}
	return ingest.Summary{}, nil
}
