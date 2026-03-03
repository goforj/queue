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

func TestLocalQueue_DispatchRunsRegisteredHandler(t *testing.T) {
	d := newLocalQueue(DriverSync)
	var calls atomic.Int64
	d.Register("job:test", func(_ context.Context, job Job) error {
		calls.Add(1)
		if job.Type != "job:test" {
			t.Fatalf("expected job type job:test, got %q", job.Type)
		}
		if string(job.PayloadBytes()) != "hello" {
			t.Fatalf("expected payload hello, got %q", string(job.PayloadBytes()))
		}
		return nil
	})

	err := d.Dispatch(context.Background(), NewJob("job:test").Payload([]byte("hello")).OnQueue("default"))
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
}

func TestLocalQueue_DispatchDelayed(t *testing.T) {
	d := newLocalQueue(DriverSync)
	triggered := make(chan struct{}, 1)
	d.Register("job:delay", func(_ context.Context, _ Job) error {
		triggered <- struct{}{}
		return nil
	})

	err := d.Dispatch(context.Background(), NewJob("job:delay").OnQueue("default").Delay(25*time.Millisecond))
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected delayed job to execute")
	}
}

func TestLocalQueue_DispatchMissingHandlerFails(t *testing.T) {
	d := newLocalQueue(DriverSync)
	err := d.Dispatch(context.Background(), NewJob("missing").OnQueue("default"))
	if err == nil {
		t.Fatal("expected error for missing handler")
	}
}

func TestLocalQueue_DispatchMissingTypeFails(t *testing.T) {
	d := newLocalQueue(DriverSync)
	err := d.Dispatch(context.Background(), NewJob("").OnQueue("default"))
	if err == nil {
		t.Fatal("expected missing job type error")
	}
}

func TestLocalQueue_DispatchWithUnique(t *testing.T) {
	d := newLocalQueue(DriverSync)
	var calls atomic.Int64
	d.Register("job:unique", func(_ context.Context, _ Job) error {
		calls.Add(1)
		return nil
	})

	jobType := "job:unique"
	payload := []byte("payload")
	err := d.Dispatch(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(120*time.Millisecond))
	if err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}

	err = d.Dispatch(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(120*time.Millisecond))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call before ttl expiry, got %d", calls.Load())
	}

	time.Sleep(150 * time.Millisecond)
	err = d.Dispatch(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(120*time.Millisecond))
	if err != nil {
		t.Fatalf("expected dispatch after ttl expiry to succeed, got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls after ttl expiry, got %d", calls.Load())
	}
}

func TestLocalQueue_WorkerpoolDispatchRunsOnWorkers(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "2")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalQueue(DriverWorkerpool)
	triggered := make(chan struct{}, 1)
	d.Register("job:workerpool", func(_ context.Context, _ Job) error {
		triggered <- struct{}{}
		return nil
	})

	if err := d.Dispatch(context.Background(), NewJob("job:workerpool").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-triggered:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected workerpool to process queued job")
	}
}

func TestLocalQueue_WorkerpoolDispatchMissingHandlerFails(t *testing.T) {
	d := newLocalQueue(DriverWorkerpool)
	err := d.Dispatch(context.Background(), NewJob("job:missing").OnQueue("default"))
	if err == nil {
		t.Fatal("expected missing handler error")
	}
}

func TestLocalQueue_WorkerpoolShutdownWaitsForRunningJobs(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalQueue(DriverWorkerpool)
	finished := make(chan struct{})
	d.Register("job:slow", func(_ context.Context, _ Job) error {
		time.Sleep(80 * time.Millisecond)
		close(finished)
		return nil
	})

	if err := d.Dispatch(context.Background(), NewJob("job:slow").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
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

func TestLocalQueue_WorkerpoolShutdownRejectsNewDispatch(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	d := newLocalQueue(DriverWorkerpool)

	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	d.Register("job:after-shutdown", func(_ context.Context, _ Job) error { return nil })
	err := d.Dispatch(context.Background(), NewJob("job:after-shutdown").OnQueue("default"))
	if err == nil {
		t.Fatal("expected dispatch to fail after shutdown")
	}
}

func TestLocalQueue_WorkerpoolSelfHealsQueueWhenNil(t *testing.T) {
	t.Setenv("QUEUE_WORKERPOOL_WORKERS", "1")
	t.Setenv("QUEUE_WORKERPOOL_BUFFER", "4")

	d := newLocalQueue(DriverWorkerpool)
	triggered := make(chan struct{}, 1)
	d.Register("job:heal-queue", func(_ context.Context, _ Job) error {
		triggered <- struct{}{}
		return nil
	})

	d.queueMu.Lock()
	d.workQueue = nil
	d.queueMu.Unlock()

	if err := d.Dispatch(context.Background(), NewJob("job:heal-queue").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed after queue reset: %v", err)
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
	d.Register("job:panic-then-ok", func(_ context.Context, _ Job) error {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		triggered <- struct{}{}
		return nil
	})

	if err := d.Dispatch(context.Background(), NewJob("job:panic-then-ok").OnQueue("default")); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	if err := d.Dispatch(context.Background(), NewJob("job:panic-then-ok").OnQueue("default")); err != nil {
		t.Fatalf("second dispatch failed: %v", err)
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
	d.Register("job:retry-sync", func(_ context.Context, _ Job) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		done <- struct{}{}
		return nil
	})

	err := d.Dispatch(context.Background(), NewJob("job:retry-sync").OnQueue("default").Retry(3).Backoff(5*time.Millisecond))
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
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
	d.Register("job:retry-workerpool", func(_ context.Context, _ Job) error {
		if calls.Add(1) < 2 {
			return errors.New("transient")
		}
		triggered <- struct{}{}
		return nil
	})

	if err := d.Dispatch(context.Background(), NewJob("job:retry-workerpool").OnQueue("default").Retry(2).Backoff(5*time.Millisecond)); err != nil {
		t.Fatalf("dispatch failed: %v", err)
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

func TestLocalQueue_WorkerpoolStatsTrackPerQueue(t *testing.T) {
	d := newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{Workers: 2, QueueCapacity: 8})
	done := make(chan string, 8)
	d.Register("job:stats-per-queue", func(_ context.Context, job Job) error {
		done <- normalizeQueueName(job.jobOptions().queueName)
		return nil
	})

	jobs := []Job{
		NewJob("job:stats-per-queue").OnQueue("critical"),
		NewJob("job:stats-per-queue").OnQueue("default"),
		NewJob("job:stats-per-queue").OnQueue("low"),
	}
	for _, job := range jobs {
		if err := d.Dispatch(context.Background(), job); err != nil {
			t.Fatalf("dispatch failed: %v", err)
		}
	}

	timeout := time.After(500 * time.Millisecond)
	for i := 0; i < len(jobs); i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("expected workerpool jobs to finish")
		}
	}

	snapshot, err := d.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats failed: %v", err)
	}
	for _, queueName := range []string{"critical", "default", "low"} {
		counters, ok := snapshot.Queue(queueName)
		if !ok {
			t.Fatalf("expected queue %q in snapshot", queueName)
		}
		if counters.Processed != 1 {
			t.Fatalf("expected processed=1 for %q, got %d", queueName, counters.Processed)
		}
		if counters.Failed != 0 {
			t.Fatalf("expected failed=0 for %q, got %d", queueName, counters.Failed)
		}
	}
}

func TestLocalQueue_SyncStatsTrackFailuresPerQueue(t *testing.T) {
	d := newLocalQueue(DriverSync)
	d.Register("job:sync-ok", func(_ context.Context, _ Job) error {
		return nil
	})
	d.Register("job:sync-fail", func(_ context.Context, _ Job) error {
		return errors.New("boom")
	})

	if err := d.Dispatch(context.Background(), NewJob("job:sync-ok").OnQueue("critical")); err != nil {
		t.Fatalf("dispatch ok job failed: %v", err)
	}
	if err := d.Dispatch(context.Background(), NewJob("job:sync-fail").OnQueue("low")); err == nil {
		t.Fatal("expected sync failure")
	}

	snapshot, err := d.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats failed: %v", err)
	}
	critical, ok := snapshot.Queue("critical")
	if !ok {
		t.Fatal("expected critical queue in snapshot")
	}
	if critical.Processed != 1 || critical.Failed != 0 {
		t.Fatalf("unexpected critical counters: %+v", critical)
	}
	low, ok := snapshot.Queue("low")
	if !ok {
		t.Fatal("expected low queue in snapshot")
	}
	if low.Processed != 0 || low.Failed != 1 {
		t.Fatalf("unexpected low counters: %+v", low)
	}
}
