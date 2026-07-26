package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/masayoshi4649/NewsStore/internal/ingest"
)

// fakeDueRepository は、定期取得対象のない Repository を表します。
type fakeDueRepository struct{}

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
ListDueSourceIDs は、期限到来した取得対象がない一覧を返します。

機能:
  - 定期実行対象が存在しない状態を模擬する空の ID 一覧を返します。

引数:
  - ctx context.Context: 未使用の DB 操作コンテキスト
  - limit int: 未使用の最大件数

返り値:
  - []int64: 空の取得対象 ID 一覧
  - error: 常に nil
*/
func (fakeDueRepository) ListDueSourceIDs(_ context.Context, _ int) ([]int64, error) {
	return []int64{}, nil
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
