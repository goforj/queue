package queue

import (
	"context"
	"sync/atomic"
	"testing"
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
	if rt.Workers(2) != rt {
		t.Fatal("Workers should return same runtime pointer")
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
		WithObserver(WorkflowObserverFunc(func(WorkflowEvent) {
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
