package queue

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntime_DispatchChainBatch_Sync(t *testing.T) {
	rt, err := New(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })

	if got := rt.Driver(); got != DriverSync {
		t.Fatalf("driver=%q expected=%q", got, DriverSync)
	}
	if rt.WithWorkers(2) != rt {
		t.Fatal("WithWorkers should return same runtime pointer")
	}

	var dispatchCalls atomic.Int32
	var chainCalls atomic.Int32
	var batchCalls atomic.Int32

	rt.Register("emails:send", func(_ context.Context, j Message) error {
		var payload struct {
			ID int `json:"id"`
		}
		if err := j.Bind(&payload); err != nil {
			return err
		}
		if payload.ID != 1 {
			t.Fatalf("unexpected dispatch payload id=%d", payload.ID)
		}
		dispatchCalls.Add(1)
		return nil
	})
	rt.Register("chain:step1", func(_ context.Context, _ Message) error {
		chainCalls.Add(1)
		return nil
	})
	rt.Register("chain:step2", func(_ context.Context, _ Message) error {
		chainCalls.Add(1)
		return nil
	})
	rt.Register("batch:step", func(_ context.Context, _ Message) error {
		batchCalls.Add(1)
		return nil
	})

	if err := rt.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	res, err := rt.Dispatch(NewJob("emails:send").Payload(struct {
		ID int `json:"id"`
	}{ID: 1}))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.DispatchID == "" {
		t.Fatal("expected non-empty dispatch id")
	}
	if dispatchCalls.Load() != 1 {
		t.Fatalf("dispatch handler calls=%d expected=1", dispatchCalls.Load())
	}

	chainID, err := rt.Chain(
		NewJob("chain:step1"),
		NewJob("chain:step2"),
	).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("chain dispatch: %v", err)
	}
	if chainID == "" {
		t.Fatal("expected non-empty chain id")
	}
	chainState, err := rt.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !chainState.Completed {
		t.Fatalf("expected completed chain, got %+v", chainState)
	}
	if chainCalls.Load() != 2 {
		t.Fatalf("chain handler calls=%d expected=2", chainCalls.Load())
	}

	batchID, err := rt.Batch(
		NewJob("batch:step"),
		NewJob("batch:step"),
	).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("batch dispatch: %v", err)
	}
	if batchID == "" {
		t.Fatal("expected non-empty batch id")
	}
	batchState, err := rt.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if !batchState.Completed || batchState.Processed != 2 {
		t.Fatalf("unexpected batch state: %+v", batchState)
	}
	if batchCalls.Load() != 2 {
		t.Fatalf("batch handler calls=%d expected=2", batchCalls.Load())
	}
}

func TestRuntime_JobValidationErrorPropagates(t *testing.T) {
	rt, err := New(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	_, err = rt.Dispatch(NewJob("bad").Timeout(-1))
	if err == nil {
		t.Fatal("expected validation error from dispatch")
	}

	if _, err := rt.Chain(NewJob("bad").Retry(-1)).Dispatch(context.Background()); err == nil {
		t.Fatal("expected validation error from chain builder dispatch")
	}
	if _, err := rt.Batch(NewJob("bad").Backoff(-1)).Dispatch(context.Background()); err == nil {
		t.Fatal("expected validation error from batch builder dispatch")
	}
}

func TestNewSync(t *testing.T) {
	rt, err := NewSync()
	if err != nil {
		t.Fatalf("new runtime sync: %v", err)
	}
	if got := rt.Driver(); got != DriverSync {
		t.Fatalf("driver=%q expected=%q", got, DriverSync)
	}
}

func TestNew_WithObserver(t *testing.T) {
	var observed atomic.Int32
	rt, err := New(
		Config{Driver: DriverSync},
		WithObserver(ObserverFunc(func(context.Context, Event) {
			observed.Add(1)
		})),
	)
	if err != nil {
		t.Fatalf("new runtime with observer: %v", err)
	}

	rt.Register("obs:test", func(context.Context, Message) error { return nil })
	if err := rt.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	if _, err := rt.Dispatch(NewJob("obs:test")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if observed.Load() == 0 {
		t.Fatal("expected workflow observer to receive events")
	}
}

// TestQueueHandlerContextDecoratorWithAndWithoutObserver verifies context decoration does not depend on observation being enabled.
func TestQueueHandlerContextDecoratorWithAndWithoutObserver(t *testing.T) {
	type contextKey struct{}
	key := contextKey{}
	const want = "jobs"

	for _, withObserver := range []bool{false, true} {
		name := "without observer"
		if withObserver {
			name = "with observer"
		}
		t.Run(name, func(t *testing.T) {
			var decoratorCalls atomic.Int32
			var observedCalls atomic.Int32
			var observerSawWrongContext atomic.Bool
			opts := []Option{
				WithHandlerContextDecorator(func(ctx context.Context) context.Context {
					decoratorCalls.Add(1)
					return context.WithValue(ctx, key, want)
				}),
			}
			if withObserver {
				opts = append(opts, WithObserver(ObserverFunc(func(ctx context.Context, event Event) {
					if event.Kind != EventProcessStarted && event.Kind != EventProcessSucceeded {
						return
					}
					observedCalls.Add(1)
					if got, _ := ctx.Value(key).(string); got != want {
						observerSawWrongContext.Store(true)
					}
				})))
			}

			q, err := NewSync(opts...)
			if err != nil {
				t.Fatalf("new sync queue: %v", err)
			}
			t.Cleanup(func() {
				if err := q.Shutdown(context.Background()); err != nil {
					t.Errorf("shutdown sync queue: %v", err)
				}
			})
			q.Register("job:decorated", func(ctx context.Context, _ Message) error {
				if got, _ := ctx.Value(key).(string); got != want {
					return errors.New("handler context was not decorated")
				}
				return nil
			})
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start workers: %v", err)
			}
			if _, err := q.Dispatch(NewJob("job:decorated")); err != nil {
				t.Fatalf("dispatch decorated job: %v", err)
			}

			if got := decoratorCalls.Load(); got != 1 {
				t.Fatalf("decorator calls = %d, want 1", got)
			}
			wantObserved := int32(0)
			if withObserver {
				wantObserved = 2
			}
			if got := observedCalls.Load(); got != wantObserved {
				t.Fatalf("observed process events = %d, want %d", got, wantObserved)
			}
			if observerSawWrongContext.Load() {
				t.Fatal("observer received a process event without the decorated context")
			}
		})
	}
}

// TestQueueSyncShutdownRetriesReuseWorkDrainGeneration verifies public retries
// cannot accumulate goroutines while one delayed Sync handler is still active.
func TestQueueSyncShutdownRetriesReuseWorkDrainGeneration(t *testing.T) {
	q, err := NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	handlerEntered := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	release := func() {
		select {
		case <-releaseHandler:
		default:
			close(releaseHandler)
		}
	}
	t.Cleanup(func() {
		release()
		if err := q.Shutdown(context.Background()); err != nil {
			t.Errorf("cleanup sync queue: %v", err)
		}
	})

	q.Register("job:delayed-shutdown", func(context.Context, Message) error {
		handlerEntered <- struct{}{}
		<-releaseHandler
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	if _, err := q.Dispatch(NewJob("job:delayed-shutdown").Delay(time.Nanosecond)); err != nil {
		t.Fatalf("dispatch delayed job: %v", err)
	}
	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("delayed handler did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first canceled shutdown error = %v, want %v", err, context.Canceled)
	}
	native, ok := q.q.(*nativeQueueRuntime)
	if !ok {
		t.Fatalf("sync queue runtime = %T, want *nativeQueueRuntime", q.q)
	}
	local, ok := native.runtime.(*localQueue)
	if !ok {
		t.Fatalf("sync backend = %T, want *localQueue", native.runtime)
	}
	local.syncWorkMu.Lock()
	sharedDone := local.syncWorkIdle
	local.syncWorkMu.Unlock()
	if sharedDone == nil {
		t.Fatal("first canceled shutdown did not retain a Sync work generation")
	}
	goroutinesAfterFirstCancel := runtime.NumGoroutine()
	for range 64 {
		if err := q.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("repeated canceled shutdown error = %v, want %v", err, context.Canceled)
		}
		local.syncWorkMu.Lock()
		currentDone := local.syncWorkIdle
		local.syncWorkMu.Unlock()
		if currentDone != sharedDone {
			t.Fatal("public shutdown retry replaced the Sync work generation")
		}
	}
	runtime.Gosched()
	if got := runtime.NumGoroutine(); got > goroutinesAfterFirstCancel+2 {
		t.Fatalf("canceled shutdown retries grew goroutines from %d to %d", goroutinesAfterFirstCancel, got)
	}
	select {
	case <-sharedDone:
		t.Fatal("Sync work generation completed while its handler was blocked")
	default:
	}

	release()
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown after handler completion: %v", err)
	}
	select {
	case <-sharedDone:
	default:
		t.Fatal("completed public shutdown did not close the shared Sync work generation")
	}
}

func TestQueue_Run_WorkerpoolStartsAndShutsDownOnCancel(t *testing.T) {
	q, err := NewWorkerpool(WithWorkers(2))
	if err != nil {
		t.Fatalf("new workerpool queue: %v", err)
	}

	done := make(chan struct{}, 1)
	q.Register("job:run:test", func(context.Context, Message) error {
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- q.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	var dispatchErr error
	for time.Now().Before(deadline) {
		_, dispatchErr = q.Dispatch(NewJob("job:run:test").OnQueue("default"))
		if dispatchErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if dispatchErr != nil {
		cancel()
		t.Fatalf("dispatch after Run start failed: %v", dispatchErr)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("handler did not run under Run lifecycle")
	}

	cancel()

	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestNew_WithStoreClockMiddlewareAndPrune(t *testing.T) {
	fixedNow := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)
	var mwCalls atomic.Int32
	var observed atomic.Int32

	q, err := New(
		Config{Driver: DriverSync},
		WithStore(NewMemoryStore()),
		WithClock(func() time.Time { return fixedNow }),
		WithObserver(ObserverFunc(func(context.Context, Event) { observed.Add(1) })),
		WithMiddleware(MiddlewareFunc(func(ctx context.Context, m Message, next Next) error {
			mwCalls.Add(1)
			return next(ctx, m)
		})),
	)
	if err != nil {
		t.Fatalf("new queue: %v", err)
	}

	q.Register("mw:test", func(context.Context, Message) error { return nil })
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	if _, err := q.Dispatch(NewJob("mw:test").OnQueue("default")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if mwCalls.Load() == 0 {
		t.Fatal("expected middleware to be invoked")
	}
	if observed.Load() == 0 {
		t.Fatal("expected workflow observer events")
	}

	chainID, err := q.Chain(NewJob("mw:test")).OnQueue("critical").Dispatch(context.Background())
	if err != nil {
		t.Fatalf("chain dispatch: %v", err)
	}
	chainState, err := q.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !chainState.CreatedAt.Equal(fixedNow) {
		t.Fatalf("expected fixed chain CreatedAt %v, got %v", fixedNow, chainState.CreatedAt)
	}
	if chainState.Queue != "critical" {
		t.Fatalf("expected chain queue critical, got %q", chainState.Queue)
	}

	batchID, err := q.Batch(NewJob("mw:test")).
		Name("nightly").
		OnQueue("bulk").
		AllowFailures().
		Progress(func(context.Context, BatchState) error { return nil }).
		Then(func(context.Context, BatchState) error { return nil }).
		Catch(func(context.Context, BatchState, error) error { return nil }).
		Finally(func(context.Context, BatchState) error { return nil }).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("batch dispatch: %v", err)
	}
	batchState, err := q.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if batchState.Name != "nightly" {
		t.Fatalf("expected batch name nightly, got %q", batchState.Name)
	}
	if batchState.Queue != "bulk" {
		t.Fatalf("expected batch queue bulk, got %q", batchState.Queue)
	}
	if !batchState.AllowFailed {
		t.Fatal("expected allow failures enabled")
	}
	if !batchState.CreatedAt.Equal(fixedNow) {
		t.Fatalf("expected fixed batch CreatedAt %v, got %v", fixedNow, batchState.CreatedAt)
	}

	if err := q.Prune(context.Background(), fixedNow.Add(24*time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}
}
