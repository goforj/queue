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
	d.Register("job:db-basic", func(_ context.Context, _ Job) error {
		triggered <- struct{}{}
		return nil
	})
	if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := d.DispatchCtx(context.Background(), NewJob("job:db-basic").Payload([]byte("hello")).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	select {
	case <-triggered:
	case <-time.After(2 * time.Second):
		t.Fatal("expected job to be processed")
	}
}

func TestDatabaseQueue_Unique(t *testing.T) {
	d := newSQLiteQueueForTest(t)
	d.Register("job:db-unique", func(_ context.Context, _ Job) error { return nil })
	if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	jobType := "job:db-unique"
	payload := []byte("same")
	if err := d.DispatchCtx(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(300*time.Millisecond)); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	if err := d.DispatchCtx(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(300*time.Millisecond)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestDatabaseQueue_RetryWithBackoff(t *testing.T) {
	d := newSQLiteQueueForTest(t)
	triggered := make(chan struct{}, 1)
	var calls atomic.Int64
	d.Register("job:db-retry", func(_ context.Context, _ Job) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		triggered <- struct{}{}
		return nil
	})
	if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := d.DispatchCtx(context.Background(), NewJob("job:db-retry").OnQueue("default").Retry(2).Backoff(20*time.Millisecond)); err != nil {
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
	consumer.Register("job:db-interop", func(_ context.Context, _ Job) error {
		done <- struct{}{}
		return nil
	})
	if err := consumer.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("consumer start failed: %v", err)
	}
	if err := producer.DispatchCtx(context.Background(), NewJob("job:db-interop").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("expected interop job to be processed")
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

	if err := qi.Dispatch(NewJob("job:pending").OnQueue("default")); err != nil {
		t.Fatalf("dispatch pending failed: %v", err)
	}
	if err := qi.Dispatch(NewJob("job:processing").OnQueue("default")); err != nil {
		t.Fatalf("dispatch processing failed: %v", err)
	}
	if err := qi.Dispatch(NewJob("job:dead").OnQueue("default")); err != nil {
		t.Fatalf("dispatch dead failed: %v", err)
	}

	_, _ = dq.db.ExecContext(context.Background(), dq.rebind(`UPDATE queue_jobs SET state='processing' WHERE job_type=?`), "job:processing")
	_, _ = dq.db.ExecContext(context.Background(), dq.rebind(`UPDATE queue_jobs SET state='dead' WHERE job_type=?`), "job:dead")

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

func TestDatabaseQueue_RecoverStaleProcessing(t *testing.T) {
	dsn := fmt.Sprintf("%s/recover-%d.db", t.TempDir(), time.Now().UnixNano())
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

	now := time.Now().UnixMilli()
	insert := dq.rebind(`INSERT INTO queue_jobs
		(queue_name, job_type, payload, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at, processing_started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	_, err = dq.db.ExecContext(context.Background(), insert,
		"default", "job:stale-timeout", []byte("{}"),
		int64(1), 0, int64(0), 0, now-1000, "processing", now-10000, now-10000, now-10000,
	)
	if err != nil {
		t.Fatalf("insert stale timeout job failed: %v", err)
	}

	_, err = dq.db.ExecContext(context.Background(), insert,
		"default", "job:fresh-timeout", []byte("{}"),
		int64(60), 0, int64(0), 0, now-1000, "processing", now-1000, now-1000, now-500,
	)
	if err != nil {
		t.Fatalf("insert fresh timeout job failed: %v", err)
	}

	if err := dq.recoverStaleProcessing(context.Background(), now); err != nil {
		t.Fatalf("recover stale processing failed: %v", err)
	}

	type rowState struct {
		state             string
		processingStarted *int64
		lastError         *string
	}
	read := func(jobType string) rowState {
		var s rowState
		row := dq.db.QueryRowContext(context.Background(), dq.rebind(`SELECT state, processing_started_at, last_error FROM queue_jobs WHERE job_type=?`), jobType)
		if err := row.Scan(&s.state, &s.processingStarted, &s.lastError); err != nil {
			t.Fatalf("scan %s: %v", jobType, err)
		}
		return s
	}

	stale := read("job:stale-timeout")
	if stale.state != "pending" {
		t.Fatalf("expected stale job to be requeued pending, got %q", stale.state)
	}
	if stale.processingStarted != nil {
		t.Fatalf("expected stale processing_started_at cleared, got %v", *stale.processingStarted)
	}

	fresh := read("job:fresh-timeout")
	if fresh.state != "processing" {
		t.Fatalf("expected fresh job to remain processing, got %q", fresh.state)
	}
}

func TestDatabaseConfig_NormalizeRecoveryDefaults(t *testing.T) {
	cfg := (DatabaseConfig{}).normalize()
	if cfg.ProcessingRecoveryGrace <= 0 {
		t.Fatalf("expected positive recovery grace, got %s", cfg.ProcessingRecoveryGrace)
	}
	if cfg.ProcessingLeaseNoTimeout <= 0 {
		t.Fatalf("expected positive no-timeout lease, got %s", cfg.ProcessingLeaseNoTimeout)
	}
}

func TestDatabaseQueue_RecoverStaleProcessing_EmitsObserverEvent(t *testing.T) {
	var events []Event
	d := &databaseQueue{
		cfg: DatabaseConfig{
			DriverName:               "sqlite",
			ProcessingRecoveryGrace:  10 * time.Millisecond,
			ProcessingLeaseNoTimeout: time.Second,
		}.normalize(),
		db:       nil,
		observer: ObserverFunc(func(e Event) { events = append(events, e) }),
	}
	// Attach a real sqlite DB via helper queue to reuse schema setup.
	qi := newSQLiteQueueForTest(t)
	rt := qi.(*nativeQueueRuntime)
	dq := rt.common.inner.(*databaseQueue)
	d.db = dq.db
	if err := d.ensureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema failed: %v", err)
	}
	now := time.Now().UnixMilli()
	insert := d.rebind(`INSERT INTO queue_jobs
		(queue_name, job_type, payload, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at, processing_started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := d.db.ExecContext(context.Background(), insert,
		"default", "job:recover-event", []byte("{}"),
		int64(1), 0, int64(0), 0, now-1000, "processing", now-2000, now-2000, now-5000,
	); err != nil {
		t.Fatalf("insert stale job failed: %v", err)
	}
	if err := d.recoverStaleProcessing(context.Background(), now); err != nil {
		t.Fatalf("recover stale processing failed: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Kind == EventProcessRecovered && e.Driver == DriverDatabase {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected process_recovered observer event")
	}
}
