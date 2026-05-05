package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/bus"
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
		WithObserver(WorkflowObserverFunc(func(context.Context, WorkflowEvent) {
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
		WithStore(bus.NewMemoryStore()),
		WithClock(func() time.Time { return fixedNow }),
		WithObserver(WorkflowObserverFunc(func(context.Context, WorkflowEvent) { observed.Add(1) })),
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
