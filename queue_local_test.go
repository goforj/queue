package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
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

// TestLocalQueueSyncShutdownSucceedsWhenCanceledAndIdle prevents an expired caller budget from failing already-complete cleanup.
func TestLocalQueueSyncShutdownSucceedsWhenCanceledAndIdle(t *testing.T) {
	d := newLocalQueue(DriverSync)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("idle sync shutdown failed: %v", err)
	}
	if !d.shuttingDown.Load() {
		t.Fatal("idle sync shutdown did not latch shutdown state")
	}

	d.Register("job:after-idle-shutdown", func(context.Context, Job) error { return nil })
	err := d.Dispatch(context.Background(), NewJob("job:after-idle-shutdown"))
	if !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("dispatch after idle shutdown error = %v, want %v", err, ErrQueuerShuttingDown)
	}
}

// TestLocalQueueSyncShutdownHonorsCancellationWithPendingWork keeps a pending drain generation bounded and retryable.
func TestLocalQueueSyncShutdownHonorsCancellationWithPendingWork(t *testing.T) {
	d := newLocalQueue(DriverSync)
	if err := d.reserveSyncWork(context.Background()); err != nil {
		t.Fatalf("reserve sync work: %v", err)
	}
	pending := true
	t.Cleanup(func() {
		if pending {
			d.finishSyncWork()
		}
	})
	d.syncWorkMu.Lock()
	sharedDone := d.syncWorkIdle
	d.syncWorkMu.Unlock()
	if sharedDone == nil {
		t.Fatal("pending sync work did not open a drain generation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pending sync shutdown error = %v, want %v", err, context.Canceled)
	}
	for range 32 {
		if err := d.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("repeated pending sync shutdown error = %v, want %v", err, context.Canceled)
		}
		d.syncWorkMu.Lock()
		currentDone := d.syncWorkIdle
		d.syncWorkMu.Unlock()
		if currentDone != sharedDone {
			t.Fatal("pending sync shutdown retry replaced the work drain generation")
		}
	}
	if !d.shuttingDown.Load() {
		t.Fatal("canceled sync shutdown did not latch shutdown state")
	}
	d.Register("job:after-canceled-shutdown", func(context.Context, Job) error { return nil })
	if err := d.Dispatch(context.Background(), NewJob("job:after-canceled-shutdown")); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("dispatch after canceled shutdown error = %v, want %v", err, ErrQueuerShuttingDown)
	}

	d.finishSyncWork()
	pending = false
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("sync shutdown retry failed after work completed: %v", err)
	}
	select {
	case <-sharedDone:
	default:
		t.Fatal("completed sync work did not close its drain generation")
	}
}

// continuationGateContext reports when Dispatch has observed its initial live continuation permit.
type continuationGateContext struct {
	context.Context
	passed chan<- struct{}
}

// Value preserves the wrapped context while exposing the otherwise internal continuation lookup as a deterministic test seam.
func (c continuationGateContext) Value(key any) any {
	value := c.Context.Value(key)
	if value != nil {
		select {
		case c.passed <- struct{}{}:
		default:
		}
	}
	return value
}

// TestLocalQueueRejectsContinuationThatEscapesBeforeReservation verifies shutdown cannot be overtaken by a child that passed the initial permit gate but lost ownership before reserving work.
func TestLocalQueueRejectsContinuationThatEscapesBeforeReservation(t *testing.T) {
	tests := []struct {
		name    string
		driver  Driver
		delayed bool
	}{
		{name: "sync immediate", driver: DriverSync},
		{name: "sync delayed", driver: DriverSync, delayed: true},
		{name: "workerpool immediate", driver: DriverWorkerpool},
		{name: "workerpool delayed", driver: DriverWorkerpool, delayed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := newLocalQueueWithConfig(test.driver, WorkerpoolConfig{Workers: 1, QueueCapacity: 1})
			var handlerCalls atomic.Int64
			jobType := "job:escaped:" + test.name
			d.Register(jobType, func(context.Context, Job) error {
				handlerCalls.Add(1)
				return nil
			})

			parentPending := true
			switch test.driver {
			case DriverSync:
				if err := d.reserveSyncWork(context.Background()); err != nil {
					t.Fatalf("reserve parent Sync work: %v", err)
				}
			case DriverWorkerpool:
				if _, err := d.reserveWorkerQueue(context.Background()); err != nil {
					t.Fatalf("reserve parent Workerpool work: %v", err)
				}
			}

			permitCtx, releasePermit := d.continuation.Permit(context.Background())
			permitActive := true
			muHeld := false
			t.Cleanup(func() {
				if permitActive {
					releasePermit()
				}
				if muHeld {
					d.mu.Unlock()
				}
				if parentPending {
					if test.driver == DriverSync {
						d.finishSyncWork()
					} else {
						d.finishQueuedWork()
					}
				}
				if err := d.Shutdown(context.Background()); err != nil {
					t.Errorf("cleanup shutdown: %v", err)
				}
			})

			canceledCtx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := d.Shutdown(canceledCtx); !errors.Is(err, context.Canceled) {
				t.Fatalf("establish draining state error = %v, want %v", err, context.Canceled)
			}

			passedInitialGate := make(chan struct{}, 1)
			childResult := make(chan error, 1)
			job := NewJob(jobType).
				Payload([]byte(test.name)).
				OnQueue("default").
				UniqueFor(time.Hour)
			if test.delayed {
				job = job.Delay(time.Millisecond)
			}

			d.mu.Lock()
			muHeld = true
			go func() {
				childResult <- d.Dispatch(continuationGateContext{Context: permitCtx, passed: passedInitialGate}, job)
			}()
			select {
			case <-passedInitialGate:
			case <-time.After(5 * time.Second):
				t.Fatal("child did not pass the initial continuation gate")
			}

			releasePermit()
			permitActive = false
			if test.driver == DriverSync {
				d.finishSyncWork()
			} else {
				d.finishQueuedWork()
			}
			parentPending = false
			if err := d.Shutdown(context.Background()); err != nil {
				t.Fatalf("finish parent drain: %v", err)
			}
			d.mu.Unlock()
			muHeld = false

			select {
			case err := <-childResult:
				if !errors.Is(err, ErrQueuerShuttingDown) {
					t.Fatalf("escaped continuation error = %v, want %v", err, ErrQueuerShuttingDown)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("escaped continuation did not finish")
			}
			if got := handlerCalls.Load(); got != 0 {
				t.Fatalf("escaped continuation handler calls = %d, want 0", got)
			}
			if got := d.delayed.Load(); got != 0 {
				t.Fatalf("escaped continuation delayed jobs = %d, want 0", got)
			}
			key := DriverUniqueKey(job, "default")
			if _, ok := d.unique.Acquire(key, time.Hour); !ok {
				t.Fatal("escaped continuation retained its uniqueness claim")
			}
		})
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

// TestLocalQueueUniqueClaimCompensatesRejectedEnqueue verifies a failed acceptance cannot poison the TTL window.
func TestLocalQueueUniqueClaimCompensatesRejectedEnqueue(t *testing.T) {
	d := newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{Workers: 1})
	d.Register("job:unique:rejected", func(context.Context, Job) error { return nil })
	d.queueMu.Lock()
	d.workQueue = make(chan queuedJob)
	d.queueMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job := NewJob("job:unique:rejected").Payload([]byte("same")).OnQueue("default").UniqueFor(time.Minute)
	if err := d.Dispatch(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("rejected dispatch error = %v, want context canceled", err)
	}
	key := DriverUniqueKey(job, "default")
	if _, ok := d.unique.Acquire(key, time.Minute); !ok {
		t.Fatal("rejected dispatch retained its uniqueness claim")
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

// TestLocalQueue_PermanentErrorStopsRetries verifies terminal application failures do not consume the remaining retry budget.
func TestLocalQueue_PermanentErrorStopsRetries(t *testing.T) {
	d := newLocalQueue(DriverSync)
	cause := errors.New("invalid recipient")
	var calls atomic.Int64
	d.Register("job:permanent", func(_ context.Context, _ Job) error {
		calls.Add(1)
		return busruntime.Permanent(cause)
	})

	err := d.Dispatch(context.Background(), NewJob("job:permanent").Retry(5))
	if !busruntime.IsPermanent(err) || !errors.Is(err, cause) {
		t.Fatalf("dispatch error = %v, want permanent cause", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

// TestLocalQueue_UncommittedErrorRedeliversSameAttempt verifies infrastructure failures do not consume application retries.
func TestLocalQueue_UncommittedErrorRedeliversSameAttempt(t *testing.T) {
	d := newLocalQueue(DriverSync)
	infrastructureErr := errors.New("workflow store unavailable")
	transientErr := errors.New("application failed")
	attempts := make([]int, 0, 3)
	d.Register("job:redeliver", func(_ context.Context, job Job) error {
		attempts = append(attempts, job.jobOptions().attempt)
		switch len(attempts) {
		case 1:
			return busruntime.Uncommitted(infrastructureErr)
		case 2:
			return transientErr
		default:
			return nil
		}
	})

	if err := d.Dispatch(context.Background(), NewJob("job:redeliver").Retry(1)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	want := []int{0, 0, 1}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for index := range want {
		if attempts[index] != want[index] {
			t.Fatalf("attempts = %v, want %v", attempts, want)
		}
	}
}

// TestLocalQueue_UncommittedRedeliveryHonorsCancellation verifies local infrastructure redelivery cannot spin after its caller stops waiting.
func TestLocalQueue_UncommittedRedeliveryHonorsCancellation(t *testing.T) {
	d := newLocalQueue(DriverSync)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	d.Register("job:redeliver:cancel", func(_ context.Context, _ Job) error {
		calls.Add(1)
		cancel()
		return busruntime.Uncommitted(errors.New("workflow store unavailable"))
	})

	err := d.Dispatch(ctx, NewJob("job:redeliver:cancel").Retry(3))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatch error = %v, want context canceled", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
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

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		snapshot, err := d.Stats(context.Background())
		if err != nil {
			t.Fatalf("stats failed: %v", err)
		}
		allProcessed := true
		for _, queueName := range []string{"critical", "default", "low"} {
			counters, ok := snapshot.Queue(queueName)
			if !ok {
				t.Fatalf("expected queue %q in snapshot", queueName)
			}
			if counters.Processed != 1 {
				allProcessed = false
			}
			if counters.Failed != 0 {
				t.Fatalf("expected failed=0 for %q, got %d", queueName, counters.Failed)
			}
		}
		if allProcessed {
			break
		}
		if time.Now().After(deadline) {
			for _, queueName := range []string{"critical", "default", "low"} {
				counters, _ := snapshot.Queue(queueName)
				t.Fatalf("expected processed=1 for %q, got %d", queueName, counters.Processed)
			}
		}
		time.Sleep(5 * time.Millisecond)
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

// TestWorkerpoolShutdownDrainsWorkflowDescendants verifies channel closure waits until an active chain can enqueue and run its next node.
func TestWorkerpoolShutdownDrainsWorkflowDescendants(t *testing.T) {
	testWorkerpoolShutdownDrainsWorkflowDescendants(t, WithWorkers(1))
}

// TestWorkerpoolShutdownDrainsWorkflowDescendantsWithReplacementDecorator
// verifies a replacement handler context cannot discard backend continuation authority.
func TestWorkerpoolShutdownDrainsWorkflowDescendantsWithReplacementDecorator(t *testing.T) {
	for _, withObserver := range []bool{false, true} {
		name := "without observer"
		opts := []Option{
			WithWorkers(1),
			WithHandlerContextDecorator(func(context.Context) context.Context {
				return context.Background()
			}),
		}
		if withObserver {
			name = "with observer"
			opts = append(opts, WithObserver(ObserverFunc(func(context.Context, Event) {})))
		}
		t.Run(name, func(t *testing.T) {
			testWorkerpoolShutdownDrainsWorkflowDescendants(t, opts...)
		})
	}
}

// testWorkerpoolShutdownDrainsWorkflowDescendants exercises workflow drain
// through the public Queue while using runtime state only as a deterministic gate.
func testWorkerpoolShutdownDrainsWorkflowDescendants(t *testing.T, opts ...Option) {
	t.Helper()
	q, err := NewWorkerpool(opts...)
	if err != nil {
		t.Fatalf("new workerpool: %v", err)
	}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRan := make(chan struct{})
	q.Register("shutdown:chain:first", func(context.Context, Message) error {
		close(firstStarted)
		<-releaseFirst
		return nil
	})
	q.Register("shutdown:chain:second", func(context.Context, Message) error {
		close(secondRan)
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	chainID, err := q.Chain(
		NewJob("shutdown:chain:first"),
		NewJob("shutdown:chain:second").Delay(25*time.Millisecond),
	).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	<-firstStarted
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- q.Shutdown(context.Background()) }()
	runtime := q.q.(*nativeQueueRuntime)
	local := runtime.runtime.(*localQueue)
	if local.cfg.Workers != 1 || local.cfg.QueueCapacity != 1 {
		t.Fatalf("configured workerpool = workers:%d capacity:%d, want 1/1", local.cfg.Workers, local.cfg.QueueCapacity)
	}
	drainDeadline := time.Now().Add(2 * time.Second)
	for {
		runtime.mu.Lock()
		draining := runtime.draining
		runtime.mu.Unlock()
		if draining && local.shuttingDown.Load() {
			break
		}
		if time.Now().After(drainDeadline) {
			t.Fatal("timed out waiting for root and workerpool drain gates")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFirst)
	select {
	case <-secondRan:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown stranded the descendant chain node")
	}
	select {
	case shutdownErr := <-shutdownResult:
		if shutdownErr != nil {
			t.Fatalf("shutdown: %v", shutdownErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after descendant work quiesced")
	}
	state, err := q.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !state.Completed || state.Failed || state.NextIndex != 2 {
		t.Fatalf("chain state after shutdown = %+v", state)
	}
}

// TestWorkerpoolTerminalCallbacksDoNotDeadlockBoundedQueue verifies one handler can schedule sibling callbacks when its only queue slot is already occupied.
func TestWorkerpoolTerminalCallbacksDoNotDeadlockBoundedQueue(t *testing.T) {
	q, err := NewWorkerpool(WithWorkers(1))
	if err != nil {
		t.Fatalf("new workerpool: %v", err)
	}
	q.Register("shutdown:batch:failure", func(context.Context, Message) error {
		return errors.New("terminal batch failure")
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	catchRan := make(chan struct{}, 1)
	finallyRan := make(chan struct{}, 1)
	batchID, err := q.Batch(NewJob("shutdown:batch:failure").Retry(0)).
		Catch(func(context.Context, BatchState, error) error {
			catchRan <- struct{}{}
			return nil
		}).
		Finally(func(context.Context, BatchState) error {
			finallyRan <- struct{}{}
			return nil
		}).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	for name, callback := range map[string]<-chan struct{}{"catch": catchRan, "finally": finallyRan} {
		select {
		case <-callback:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s callback deadlocked behind bounded worker queue", name)
		}
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	state, err := q.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if !state.Completed || !state.Cancelled || state.Failed != 1 {
		t.Fatalf("terminal batch state = %+v", state)
	}
}
