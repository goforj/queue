package queue

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func newSQLiteDispatcherForTest(t *testing.T) Dispatcher {
	t.Helper()
	dbPath := fmt.Sprintf("%s/queue-%d.db", t.TempDir(), time.Now().UnixNano())
	dispatcher, err := NewDatabaseDispatcher(DatabaseConfig{
		DriverName:   "sqlite",
		DSN:          dbPath,
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new database dispatcher failed: %v", err)
	}
	t.Cleanup(func() {
		_ = dispatcher.Shutdown(context.Background())
	})
	return dispatcher
}

func TestDatabaseDispatcher_EnqueueAndProcess(t *testing.T) {
	d := newSQLiteDispatcherForTest(t)
	triggered := make(chan struct{}, 1)
	d.Register("job:db-basic", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := d.Enqueue(context.Background(), Task{Type: "job:db-basic", Payload: []byte("hello")}); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	select {
	case <-triggered:
	case <-time.After(2 * time.Second):
		t.Fatal("expected task to be processed")
	}
}

func TestDatabaseDispatcher_Unique(t *testing.T) {
	d := newSQLiteDispatcherForTest(t)
	d.Register("job:db-unique", func(_ context.Context, _ Task) error { return nil })
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	task := Task{Type: "job:db-unique", Payload: []byte("same")}
	if err := d.Enqueue(context.Background(), task, WithUnique(300*time.Millisecond)); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	if err := d.Enqueue(context.Background(), task, WithUnique(300*time.Millisecond)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestDatabaseDispatcher_RetryWithBackoff(t *testing.T) {
	d := newSQLiteDispatcherForTest(t)
	triggered := make(chan struct{}, 1)
	var calls atomic.Int64
	d.Register("job:db-retry", func(_ context.Context, _ Task) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		triggered <- struct{}{}
		return nil
	})
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := d.Enqueue(
		context.Background(),
		Task{Type: "job:db-retry"},
		WithMaxRetry(2),
		WithBackoff(20*time.Millisecond),
	); err != nil {
		t.Fatalf("enqueue failed: %v", err)
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
