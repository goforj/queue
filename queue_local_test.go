package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalQueue_Driver(t *testing.T) {
	d := newLocalQueue(DriverSync)
	if got := d.Driver(); got != DriverSync {
		t.Fatalf("expected sync driver, got %q", got)
	}
}

func TestLocalQueue_EnqueueRunsRegisteredHandler(t *testing.T) {
	d := newLocalQueue(DriverSync)
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

	err := d.Enqueue(context.Background(), NewTask("job:test").Payload([]byte("hello")).OnQueue("default"))
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
}

func TestLocalQueue_EnqueueDelayed(t *testing.T) {
	d := newLocalQueue(DriverSync)
	triggered := make(chan struct{}, 1)
	d.Register("job:delay", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})

	err := d.Enqueue(context.Background(), NewTask("job:delay").OnQueue("default").Delay(25*time.Millisecond))
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected delayed task to execute")
	}
}

func TestLocalQueue_EnqueueMissingHandlerFails(t *testing.T) {
	d := newLocalQueue(DriverSync)
	err := d.Enqueue(context.Background(), NewTask("missing").OnQueue("default"))
	if err == nil {
		t.Fatal("expected error for missing handler")
	}
}

func TestLocalQueue_EnqueueMissingTypeFails(t *testing.T) {
	d := newLocalQueue(DriverSync)
	err := d.Enqueue(context.Background(), NewTask("").OnQueue("default"))
	if err == nil {
		t.Fatal("expected missing task type error")
	}
}

func TestLocalQueue_EnqueueWithUnique(t *testing.T) {
	d := newLocalQueue(DriverSync)
	var calls atomic.Int64
	d.Register("job:unique", func(_ context.Context, _ Task) error {
		calls.Add(1)
		return nil
	})

	taskType := "job:unique"
	payload := []byte("payload")
	err := d.Enqueue(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(120*time.Millisecond))
	if err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	err = d.Enqueue(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(120*time.Millisecond))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call before ttl expiry, got %d", calls.Load())
	}

	time.Sleep(150 * time.Millisecond)
	err = d.Enqueue(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(120*time.Millisecond))
	if err != nil {
		t.Fatalf("expected enqueue after ttl expiry to succeed, got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls after ttl expiry, got %d", calls.Load())
	}
}

func TestLocalQueue_WorkerpoolEnqueueRunsOnWorkers(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "2")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalQueue(DriverWorkerpool)
	triggered := make(chan struct{}, 1)
	d.Register("job:workerpool", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})

	if err := d.Enqueue(context.Background(), NewTask("job:workerpool").OnQueue("default")); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected workerpool to process queued job")
	}
}

func TestLocalQueue_WorkerpoolEnqueueMissingHandlerFails(t *testing.T) {
	d := newLocalQueue(DriverWorkerpool)
	err := d.Enqueue(context.Background(), NewTask("job:missing").OnQueue("default"))
	if err == nil {
		t.Fatal("expected missing handler error")
	}
}

func TestLocalQueue_WorkerpoolShutdownWaitsForRunningJobs(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalQueue(DriverWorkerpool)
	finished := make(chan struct{})
	d.Register("job:slow", func(_ context.Context, _ Task) error {
		time.Sleep(80 * time.Millisecond)
		close(finished)
		return nil
	})

	if err := d.Enqueue(context.Background(), NewTask("job:slow").OnQueue("default")); err != nil {
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

func TestLocalQueue_WorkerpoolShutdownRejectsNewEnqueue(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	d := newLocalQueue(DriverWorkerpool)

	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	d.Register("job:after-shutdown", func(_ context.Context, _ Task) error { return nil })
	err := d.Enqueue(context.Background(), NewTask("job:after-shutdown").OnQueue("default"))
	if err == nil {
		t.Fatal("expected enqueue to fail after shutdown")
	}
}

func TestLocalQueue_WorkerpoolSelfHealsQueueWhenNil(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalQueue(DriverWorkerpool)
	triggered := make(chan struct{}, 1)
	d.Register("job:heal-queue", func(_ context.Context, _ Task) error {
		triggered <- struct{}{}
		return nil
	})

	d.queueMu.Lock()
	d.workQueue = nil
	d.queueMu.Unlock()

	if err := d.Enqueue(context.Background(), NewTask("job:heal-queue").OnQueue("default")); err != nil {
		t.Fatalf("enqueue failed after queue reset: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected self-healed workerpool to process queued job")
	}
}

func TestLocalQueue_WorkerpoolRecoversWorkerAfterPanic(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalQueue(DriverWorkerpool)
	var calls atomic.Int64
	triggered := make(chan struct{}, 1)
	d.Register("job:panic-then-ok", func(_ context.Context, _ Task) error {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		triggered <- struct{}{}
		return nil
	})

	if err := d.Enqueue(context.Background(), NewTask("job:panic-then-ok").OnQueue("default")); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	if err := d.Enqueue(context.Background(), NewTask("job:panic-then-ok").OnQueue("default")); err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected worker to continue processing after panic")
	}
}

func TestLocalQueue_SyncRetriesWithBackoff(t *testing.T) {
	d := newLocalQueue(DriverSync)
	var calls atomic.Int64
	done := make(chan struct{}, 1)
	d.Register("job:retry-sync", func(_ context.Context, _ Task) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		done <- struct{}{}
		return nil
	})

	err := d.Enqueue(context.Background(), NewTask("job:retry-sync").OnQueue("default").Retry(3).Backoff(5*time.Millisecond))
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

func TestLocalQueue_WorkerpoolRetriesWithBackoff(t *testing.T) {
	d := newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{Workers: 1, QueueCapacity: 4})
	triggered := make(chan struct{}, 1)
	var calls atomic.Int64
	d.Register("job:retry-workerpool", func(_ context.Context, _ Task) error {
		if calls.Add(1) < 2 {
			return errors.New("transient")
		}
		triggered <- struct{}{}
		return nil
	})

	if err := d.Enqueue(context.Background(), NewTask("job:retry-workerpool").OnQueue("default").Retry(2).Backoff(5*time.Millisecond)); err != nil {
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
