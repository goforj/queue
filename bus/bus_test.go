package bus_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

func TestDispatchExecutesRegisteredHandler(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	b, err := bus.New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var called bool
	b.Register("monitor:poll", func(ctx context.Context, c bus.Context) error {
		called = true
		return nil
	})

	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", map[string]string{"url": "https://x"})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !called {
		t.Fatal("expected handler to be called")
	}
}

func TestChainStopsOnFailureAndRunsCallbacksOnce(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	b, err := bus.New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	mu := sync.Mutex{}
	order := make([]string, 0, 3)
	var catchCount int
	var finallyCount int

	b.Register("monitor:poll", func(ctx context.Context, c bus.Context) error {
		mu.Lock()
		order = append(order, c.JobType)
		mu.Unlock()
		return nil
	})
	b.Register("monitor:downsample", func(ctx context.Context, c bus.Context) error {
		mu.Lock()
		order = append(order, c.JobType)
		mu.Unlock()
		return errors.New("boom")
	})
	b.Register("monitor:alert", func(ctx context.Context, c bus.Context) error {
		mu.Lock()
		order = append(order, c.JobType)
		mu.Unlock()
		return nil
	})

	if _, err := b.Chain(
		bus.NewJob("monitor:poll", nil),
		bus.NewJob("monitor:downsample", nil),
		bus.NewJob("monitor:alert", nil),
	).Catch(func(ctx context.Context, st bus.ChainState, err error) error {
		catchCount++
		return nil
	}).Finally(func(ctx context.Context, st bus.ChainState) error {
		finallyCount++
		return nil
	}).Dispatch(context.Background()); err == nil {
		t.Fatal("expected chain dispatch to surface failure")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("expected 2 executed jobs, got %d (%v)", len(order), order)
	}
	if order[0] != "monitor:poll" || order[1] != "monitor:downsample" {
		t.Fatalf("unexpected execution order: %v", order)
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once, got %d", catchCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}
}

func TestBatchTracksCompletion(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	b, err := bus.New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
	b.Register("monitor:downsample", func(context.Context, bus.Context) error { return nil })
	b.Register("monitor:alert", func(context.Context, bus.Context) error { return nil })

	var thenCount int
	var finallyCount int
	batchID, err := b.Batch(
		bus.NewJob("monitor:poll", nil),
		bus.NewJob("monitor:downsample", nil),
		bus.NewJob("monitor:alert", nil),
	).Then(func(ctx context.Context, st bus.BatchState) error {
		thenCount++
		return nil
	}).Finally(func(ctx context.Context, st bus.BatchState) error {
		finallyCount++
		return nil
	}).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}

	st, err := b.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if !st.Completed {
		t.Fatal("expected batch completed")
	}
	if st.Processed != 3 {
		t.Fatalf("expected processed=3, got %d", st.Processed)
	}
	if thenCount != 1 {
		t.Fatalf("expected then once, got %d", thenCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}
}

func TestCallbackJobEmitsCallbackEvents(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}

	var callbackStarted int
	var callbackSucceeded int
	observer := bus.ObserverFunc(func(_ context.Context, e bus.Event) {
		if e.Kind == bus.EventCallbackStarted {
			callbackStarted++
		}
		if e.Kind == bus.EventCallbackSucceeded {
			callbackSucceeded++
		}
	})

	b, err := bus.New(q, bus.WithObserver(observer))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
	b.Register("monitor:downsample", func(context.Context, bus.Context) error { return errors.New("boom") })
	b.Register("monitor:alert", func(context.Context, bus.Context) error { return nil })

	_, _ = b.Chain(
		bus.NewJob("monitor:poll", nil),
		bus.NewJob("monitor:downsample", nil),
		bus.NewJob("monitor:alert", nil),
	).Catch(func(context.Context, bus.ChainState, error) error {
		return nil
	}).Finally(func(context.Context, bus.ChainState) error {
		return nil
	}).Dispatch(context.Background())

	if callbackStarted == 0 {
		t.Fatal("expected callback started events")
	}
	if callbackSucceeded == 0 {
		t.Fatal("expected callback succeeded events")
	}
}

func TestDispatchEmitsStartedAndSucceeded(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}

	var started int
	var succeeded int
	observer := bus.ObserverFunc(func(_ context.Context, e bus.Event) {
		if e.Kind == bus.EventDispatchStarted {
			started++
		}
		if e.Kind == bus.EventDispatchSucceeded {
			succeeded++
		}
	})

	b, err := bus.New(q, bus.WithObserver(observer))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if started != 1 {
		t.Fatalf("expected dispatch started once, got %d", started)
	}
	if succeeded != 1 {
		t.Fatalf("expected dispatch succeeded once, got %d", succeeded)
	}
}

func TestBatchFailFastEmitsCancelled(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}

	var cancelled int
	observer := bus.ObserverFunc(func(_ context.Context, e bus.Event) {
		if e.Kind == bus.EventBatchCancelled {
			cancelled++
		}
	})

	b, err := bus.New(q, bus.WithObserver(observer))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	b.Register("monitor:poll", func(context.Context, bus.Context) error { return errors.New("fail-fast") })
	if _, err := b.Batch(bus.NewJob("monitor:poll", nil)).Dispatch(context.Background()); err == nil {
		t.Fatal("expected batch dispatch error")
	}
	if cancelled != 1 {
		t.Fatalf("expected batch cancelled once, got %d", cancelled)
	}
}

func TestChainCatchRunsOnceForDuplicateCallbackJobs(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	store := bus.NewMemoryStore()

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var catchCount int
	b.Register("monitor:downsample", func(context.Context, bus.Context) error {
		return errors.New("boom")
	})

	chainID, err := b.Chain(
		bus.NewJob("monitor:downsample", nil),
	).Catch(func(context.Context, bus.ChainState, error) error {
		catchCount++
		return nil
	}).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected chain dispatch error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once after failed chain, got %d", catchCount)
	}

	cbPayload := map[string]any{
		"schema_version": 1,
		"dispatch_id":    "dup-dispatch",
		"kind":           "callback",
		"job_id":         "dup-job",
		"chain_id":       chainID,
		"callback_kind":  "chain_catch",
		"error":          "boom",
	}
	if err := q.Dispatch(queue.NewJob("bus:callback").Payload(cbPayload)); err != nil {
		t.Fatalf("dispatch duplicate callback: %v", err)
	}
	if catchCount != 1 {
		t.Fatalf("expected catch to remain once after duplicate callback, got %d", catchCount)
	}
}

func TestBatchThenFinallyRunOnceForDuplicateCallbackJobs(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	store := bus.NewMemoryStore()

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var thenCount int
	var finallyCount int
	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })

	batchID, err := b.Batch(
		bus.NewJob("monitor:poll", nil),
	).Then(func(context.Context, bus.BatchState) error {
		thenCount++
		return nil
	}).Finally(func(context.Context, bus.BatchState) error {
		finallyCount++
		return nil
	}).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	if thenCount != 1 {
		t.Fatalf("expected then once, got %d", thenCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}

	for _, callbackKind := range []string{"batch_then", "batch_finally"} {
		cbPayload := map[string]any{
			"schema_version": 1,
			"dispatch_id":    "dup-dispatch",
			"kind":           "callback",
			"job_id":         "dup-job-" + callbackKind,
			"batch_id":       batchID,
			"callback_kind":  callbackKind,
		}
		if err := q.Dispatch(queue.NewJob("bus:callback").Payload(cbPayload)); err != nil {
			t.Fatalf("dispatch duplicate callback (%s): %v", callbackKind, err)
		}
	}

	if thenCount != 1 {
		t.Fatalf("expected then to remain once after duplicate callbacks, got %d", thenCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally to remain once after duplicate callbacks, got %d", finallyCount)
	}
}

func TestBatchCatchRunsOnceForDuplicateCallbackJobs(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	store := bus.NewMemoryStore()

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var catchCount int
	b.Register("monitor:downsample", func(context.Context, bus.Context) error {
		return errors.New("boom")
	})

	batchID, err := b.Batch(
		bus.NewJob("monitor:downsample", nil),
	).Catch(func(context.Context, bus.BatchState, error) error {
		catchCount++
		return nil
	}).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected batch dispatch error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once after failed batch, got %d", catchCount)
	}

	cbPayload := map[string]any{
		"schema_version": 1,
		"dispatch_id":    "dup-dispatch",
		"kind":           "callback",
		"job_id":         "dup-job-batch-catch",
		"batch_id":       batchID,
		"callback_kind":  "batch_catch",
		"error":          "boom",
	}
	if err := q.Dispatch(queue.NewJob("bus:callback").Payload(cbPayload)); err != nil {
		t.Fatalf("dispatch duplicate callback: %v", err)
	}
	if catchCount != 1 {
		t.Fatalf("expected catch to remain once after duplicate callback, got %d", catchCount)
	}
}

func TestBusPruneRemovesTerminalWorkflowState(t *testing.T) {
	q, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	store := bus.NewMemoryStore()
	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
	chainID, err := b.Chain(bus.NewJob("monitor:poll", nil)).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	if _, err := b.FindChain(context.Background(), chainID); err != nil {
		t.Fatalf("find chain before prune: %v", err)
	}

	if err := b.Prune(context.Background(), time.Now().Add(1*time.Minute)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := b.FindChain(context.Background(), chainID); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("expected chain pruned, got err=%v", err)
	}
}
