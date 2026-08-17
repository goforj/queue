package sqlitequeue

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
)

// TestSQLiteWorkerLifecycleLeavesPausedJobsPending verifies maintenance-style pauses happen before durable claims.
func TestSQLiteWorkerLifecycleLeavesPausedJobsPending(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "queue.db")
	q, err := New(dsn, queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new SQLite queue: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	handled := make(chan struct{}, 1)
	var calls atomic.Int64
	q.Register("reports:build", func(context.Context, queue.Message) error {
		calls.Add(1)
		handled <- struct{}{}
		return nil
	})
	if err := q.PauseWorkers(context.Background()); err != nil {
		t.Fatalf("pause workers before startup: %v", err)
	}
	if _, err := q.Dispatch(queue.NewJob("reports:build").OnQueue("default")); err != nil {
		t.Fatalf("dispatch while paused: %v", err)
	}
	select {
	case <-handled:
		t.Fatal("paused SQLite worker claimed the job")
	case <-time.After(150 * time.Millisecond):
	}
	stats, err := q.Stats(context.Background())
	if err != nil {
		t.Fatalf("read paused queue stats: %v", err)
	}
	counters := stats.ByQueue["default"]
	if counters.Pending != 1 || counters.Active != 0 || counters.Failed != 0 || counters.Archived != 0 {
		t.Fatalf("paused queue counters = %+v, want one untouched pending job", counters)
	}

	if err := q.ResumeWorkers(context.Background()); err != nil {
		t.Fatalf("resume workers: %v", err)
	}
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed SQLite worker did not execute the pending job")
	}
	counters = waitForSQLiteQueueSettlement(t, q)
	if calls.Load() != 1 || counters.Failed != 0 || counters.Archived != 0 {
		t.Fatalf("resumed queue counters = %+v, want one successful execution", counters)
	}

	if err := q.PauseWorkers(context.Background()); err != nil {
		t.Fatalf("pause live workers: %v", err)
	}
	if _, err := q.Dispatch(queue.NewJob("reports:build").OnQueue("default")); err != nil {
		t.Fatalf("dispatch during live pause: %v", err)
	}
	select {
	case <-handled:
		t.Fatal("live-paused SQLite worker claimed the job")
	case <-time.After(150 * time.Millisecond):
	}
	if err := q.ResumeWorkers(context.Background()); err != nil {
		t.Fatalf("resume live workers: %v", err)
	}
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("live-resumed SQLite worker did not execute the pending job")
	}
	counters = waitForSQLiteQueueSettlement(t, q)
	if calls.Load() != 2 || counters.Failed != 0 || counters.Archived != 0 {
		t.Fatalf("live-resumed queue counters = %+v, want two successful executions", counters)
	}
}

// waitForSQLiteQueueSettlement waits for the durable success update that follows handler return.
func waitForSQLiteQueueSettlement(t *testing.T, q *queue.Queue) queue.QueueCounters {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats, err := q.Stats(context.Background())
		if err != nil {
			t.Fatalf("read resumed queue stats: %v", err)
		}
		counters := stats.ByQueue["default"]
		if counters.Pending == 0 && counters.Active == 0 {
			return counters
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not settle after handler completion; counters = %+v", counters)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
