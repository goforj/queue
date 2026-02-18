package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalDispatcher_Driver(t *testing.T) {
	d := newLocalDispatcher(DriverSync)
	if got := d.Driver(); got != DriverSync {
		t.Fatalf("expected sync driver, got %q", got)
	}
}

func TestLocalDispatcher_EnqueueRunsRegisteredHandler(t *testing.T) {
	d := newLocalDispatcher(DriverSync)
	var calls atomic.Int64
	d.Register("job:test", func(_ context.Context, task Task) error {
		calls.Add(1)
		if task.Type != "job:test" {
			t.Fatalf("expected task type job:test, got %q", task.Type)
		}
		if string(task.PayloadBytes()) != "hello" {
			t.Fatalf("expected payload hello, got %q", string(task.PayloadBytes()))
		}
		return nil
	})

	err := dispatch(d, "job:test", []byte("hello"))
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
}

func TestLocalDispatcher_EnqueueDelayed(t *testing.T) {
	d := newLocalDispatcher(DriverSync)
	triggered := make(chan struct{}, 1)
	d.Register("job:delay", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})

	err := dispatch(d, "job:delay", nil, WithDelay(25*time.Millisecond))
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected delayed task to execute")
	}
}

func TestLocalDispatcher_EnqueueMissingHandlerFails(t *testing.T) {
	d := newLocalDispatcher(DriverSync)
	err := dispatch(d, "missing", nil)
	if err == nil {
		t.Fatal("expected error for missing handler")
	}
}

func TestLocalDispatcher_EnqueueMissingTypeFails(t *testing.T) {
	d := newLocalDispatcher(DriverSync)
	err := dispatch(d, "", nil)
	if err == nil {
		t.Fatal("expected missing task type error")
	}
}

func TestLocalDispatcher_EnqueueWithUnique(t *testing.T) {
	d := newLocalDispatcher(DriverSync)
	var calls atomic.Int64
	d.Register("job:unique", func(_ context.Context, _ Task) error {
		calls.Add(1)
		return nil
	})

	taskType := "job:unique"
	payload := []byte("payload")
	err := dispatch(d, taskType, payload, WithUnique(120*time.Millisecond))
	if err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	err = dispatch(d, taskType, payload, WithUnique(120*time.Millisecond))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call before ttl expiry, got %d", calls.Load())
	}

	time.Sleep(150 * time.Millisecond)
	err = dispatch(d, taskType, payload, WithUnique(120*time.Millisecond))
	if err != nil {
		t.Fatalf("expected enqueue after ttl expiry to succeed, got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls after ttl expiry, got %d", calls.Load())
	}
}

func TestLocalDispatcher_WorkerpoolEnqueueRunsOnWorkers(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "2")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalDispatcher(DriverWorkerpool)
	triggered := make(chan struct{}, 1)
	d.Register("job:workerpool", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})

	if err := dispatch(d, "job:workerpool", nil); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected workerpool to process queued job")
	}
}

func TestLocalDispatcher_WorkerpoolEnqueueMissingHandlerFails(t *testing.T) {
	d := newLocalDispatcher(DriverWorkerpool)
	err := dispatch(d, "job:missing", nil)
	if err == nil {
		t.Fatal("expected missing handler error")
	}
}

func TestLocalDispatcher_WorkerpoolShutdownWaitsForRunningJobs(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalDispatcher(DriverWorkerpool)
	finished := make(chan struct{})
	d.Register("job:slow", func(_ context.Context, _ Task) error {
		time.Sleep(80 * time.Millisecond)
		close(finished)
		return nil
	})

	if err := dispatch(d, "job:slow", nil); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- d.Shutdown(context.Background())
	}()

	select {
	case <-shutdownDone:
		t.Fatal("shutdown returned before running job completed")
	case <-time.After(25 * time.Millisecond):
	}

	select {
	case <-finished:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected workerpool job to complete")
	}

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected shutdown to return after running jobs")
	}
}

func TestLocalDispatcher_WorkerpoolShutdownRejectsNewEnqueue(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	d := newLocalDispatcher(DriverWorkerpool)

	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	d.Register("job:after-shutdown", func(_ context.Context, _ Task) error { return nil })
	err := dispatch(d, "job:after-shutdown", nil)
	if err == nil {
		t.Fatal("expected enqueue to fail after shutdown")
	}
}

func TestLocalDispatcher_WorkerpoolSelfHealsQueueWhenNil(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalDispatcher(DriverWorkerpool)
	triggered := make(chan struct{}, 1)
	d.Register("job:heal-queue", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})

	d.queueMu.Lock()
	d.workQueue = nil
	d.queueMu.Unlock()

	if err := dispatch(d, "job:heal-queue", nil); err != nil {
		t.Fatalf("enqueue failed after queue reset: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected self-healed workerpool to process queued job")
	}
}

func TestLocalDispatcher_WorkerpoolRecoversWorkerAfterPanic(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalDispatcher(DriverWorkerpool)
	var calls atomic.Int64
	triggered := make(chan struct{}, 1)
	d.Register("job:panic-then-ok", func(_ context.Context, _ Task) error {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		triggered <- struct{}{}
		return nil
	})

	if err := dispatch(d, "job:panic-then-ok", nil); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	if err := dispatch(d, "job:panic-then-ok", nil); err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected worker to continue processing after panic")
	}
}

func TestLocalDispatcher_SyncRetriesWithBackoff(t *testing.T) {
	d := newLocalDispatcher(DriverSync)
	var calls atomic.Int64
	done := make(chan struct{}, 1)
	d.Register("job:retry-sync", func(_ context.Context, _ Task) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		done <- struct{}{}
		return nil
	})

	err := dispatch(d,
		"job:retry-sync",
		nil,
		WithMaxRetry(3),
		WithBackoff(5*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
	select {
	case <-done:
	default:
		t.Fatal("expected handler success on retry")
	}
}

func TestLocalDispatcher_WorkerpoolRetriesWithBackoff(t *testing.T) {
	d := newLocalDispatcherWithConfig(DriverWorkerpool, WorkerpoolConfig{Workers: 1, QueueCapacity: 4})
	triggered := make(chan struct{}, 1)
	var calls atomic.Int64
	d.Register("job:retry-workerpool", func(_ context.Context, _ Task) error {
		if calls.Add(1) < 2 {
			return errors.New("transient")
		}
		triggered <- struct{}{}
		return nil
	})

	if err := dispatch(d,
		"job:retry-workerpool",
		nil,
		WithMaxRetry(2),
		WithBackoff(5*time.Millisecond),
	); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected workerpool retry to succeed")
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls.Load())
	}
}
