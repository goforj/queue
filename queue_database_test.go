package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newSQLiteQueueForTest(t *testing.T) Queue {
	t.Helper()
	dbPath := fmt.Sprintf("%s/queue-%d.db", t.TempDir(), time.Now().UnixNano())
	q, err := New(Config{
		Driver:         DriverDatabase,
		DatabaseDriver: "sqlite",
		DatabaseDSN:    dbPath,
	})
	if err != nil {
		t.Fatalf("new database queue failed: %v", err)
	}
	t.Cleanup(func() {
		_ = q.Shutdown(context.Background())
	})
	return q
}

func TestDatabaseQueue_DispatchAndProcess(t *testing.T) {
	d := newSQLiteQueueForTest(t)
	triggered := make(chan struct{}, 1)
	d.Register("job:db-basic", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})
	if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := d.DispatchCtx(context.Background(), NewTask("job:db-basic").Payload([]byte("hello")).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	select {
	case <-triggered:
	case <-time.After(2 * time.Second):
		t.Fatal("expected task to be processed")
	}
}

func TestDatabaseQueue_Unique(t *testing.T) {
	d := newSQLiteQueueForTest(t)
	d.Register("job:db-unique", func(_ context.Context, _ Task) error { return nil })
	if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	taskType := "job:db-unique"
	payload := []byte("same")
	if err := d.DispatchCtx(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(300*time.Millisecond)); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	if err := d.DispatchCtx(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(300*time.Millisecond)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestDatabaseQueue_RetryWithBackoff(t *testing.T) {
	d := newSQLiteQueueForTest(t)
	triggered := make(chan struct{}, 1)
	var calls atomic.Int64
	d.Register("job:db-retry", func(_ context.Context, _ Task) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		triggered <- struct{}{}
		return nil
	})
	if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := d.DispatchCtx(context.Background(), NewTask("job:db-retry").OnQueue("default").Retry(2).Backoff(20*time.Millisecond)); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	select {
	case <-triggered:
	case <-time.After(3 * time.Second):
		t.Fatal("expected retry to succeed")
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestDatabaseQueue_QueueAndWorkerInteropSQLite(t *testing.T) {
	dsn := fmt.Sprintf("%s/interop-%d.db", t.TempDir(), time.Now().UnixNano())
	consumer, err := New(Config{
		Driver:         DriverDatabase,
		DatabaseDriver: "sqlite",
		DatabaseDSN:    dsn,
	})
	if err != nil {
		t.Fatalf("new sqlite consumer queue failed: %v", err)
	}
	defer consumer.Shutdown(context.Background())

	producer, err := New(Config{
		Driver:         DriverDatabase,
		DatabaseDriver: "sqlite",
		DatabaseDSN:    dsn,
	})
	if err != nil {
		t.Fatalf("new sqlite queue failed: %v", err)
	}
	defer producer.Shutdown(context.Background())

	done := make(chan struct{}, 1)
	consumer.Register("job:db-interop", func(_ context.Context, _ Task) error {
		done <- struct{}{}
		return nil
	})
	if err := consumer.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("consumer start failed: %v", err)
	}
	if err := producer.DispatchCtx(context.Background(), NewTask("job:db-interop").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("expected interop task to be processed")
	}
}

func TestDatabaseQueue_RebindPostgresPlaceholders(t *testing.T) {
	d := &databaseQueue{cfg: DatabaseConfig{DriverName: "postgres"}}
	got := d.rebind("SELECT * FROM q WHERE id=? AND queue_name=?")
	if got != "SELECT * FROM q WHERE id=$1 AND queue_name=$2" {
		t.Fatalf("unexpected postgres rebind result: %q", got)
	}

	d.cfg.DriverName = "sqlite"
	if passthrough := d.rebind("SELECT * FROM q WHERE id=?"); !strings.Contains(passthrough, "?") {
		t.Fatalf("expected sqlite rebind passthrough, got %q", passthrough)
	}
}

func TestDatabaseQueue_StatsSnapshot(t *testing.T) {
	dsn := fmt.Sprintf("%s/stats-%d.db", t.TempDir(), time.Now().UnixNano())
	qi, err := New(Config{
		Driver:         DriverDatabase,
		DatabaseDriver: "sqlite",
		DatabaseDSN:    dsn,
	})
	if err != nil {
		t.Fatalf("new database queue failed: %v", err)
	}
	t.Cleanup(func() { _ = qi.Shutdown(context.Background()) })

	rt, ok := qi.(*nativeQueueRuntime)
	if !ok {
		t.Fatalf("expected nativeQueueRuntime, got %T", qi)
	}
	dq, ok := rt.common.inner.(*databaseQueue)
	if !ok {
		t.Fatalf("expected databaseQueue backend, got %T", rt.common.inner)
	}
	if err := dq.ensureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema failed: %v", err)
	}

	if err := qi.Dispatch(NewTask("job:pending").OnQueue("default")); err != nil {
		t.Fatalf("dispatch pending failed: %v", err)
	}
	if err := qi.Dispatch(NewTask("job:processing").OnQueue("default")); err != nil {
		t.Fatalf("dispatch processing failed: %v", err)
	}
	if err := qi.Dispatch(NewTask("job:dead").OnQueue("default")); err != nil {
		t.Fatalf("dispatch dead failed: %v", err)
	}

	_, _ = dq.db.ExecContext(context.Background(), dq.rebind(`UPDATE queue_jobs SET state='processing' WHERE task_type=?`), "job:processing")
	_, _ = dq.db.ExecContext(context.Background(), dq.rebind(`UPDATE queue_jobs SET state='dead' WHERE task_type=?`), "job:dead")

	snap, err := dq.Stats(nil)
	if err != nil {
		t.Fatalf("stats failed: %v", err)
	}
	if got := snap.Pending("default"); got != 1 {
		t.Fatalf("expected pending=1, got %d", got)
	}
	if got := snap.Active("default"); got != 1 {
		t.Fatalf("expected active=1, got %d", got)
	}
	if got := snap.Archived("default"); got != 1 {
		t.Fatalf("expected archived=1, got %d", got)
	}
	if got := snap.Failed("default"); got != 1 {
		t.Fatalf("expected failed=1, got %d", got)
	}
}
