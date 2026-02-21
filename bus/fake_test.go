package bus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue/bus"
)

func TestFakeAssertions(t *testing.T) {
	f := bus.NewFake()
	f.AssertNothingDispatched(t)
	f.AssertNothingBatched(t)
	f.AssertBatchCount(t, 0)

	_, _ = f.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil))
	_, _ = f.Dispatch(context.Background(), bus.Job{
		Type: "monitor:poll",
		Options: bus.JobOptions{
			Queue: "monitor-critical",
		},
	})
	_, _ = f.Dispatch(context.Background(), bus.NewJob("monitor:alert", nil))

	f.AssertCount(t, 3)
	f.AssertDispatched(t, "monitor:poll")
	f.AssertDispatchedOn(t, "monitor-critical", "monitor:poll")
	f.AssertDispatchedTimes(t, "monitor:poll", 2)
	f.AssertNotDispatched(t, "monitor:downsample")

	_, _ = f.Chain(
		bus.NewJob("monitor:poll", nil),
		bus.NewJob("monitor:downsample", nil),
		bus.NewJob("monitor:alert", nil),
	).Dispatch(context.Background())
	f.AssertChained(t, []string{"monitor:poll", "monitor:downsample", "monitor:alert"})

	_, _ = f.Batch(
		bus.NewJob("monitor:poll", nil),
		bus.NewJob("monitor:downsample", nil),
	).Dispatch(context.Background())
	f.AssertBatchCount(t, 1)
	f.AssertBatched(t, func(spec bus.BatchSpec) bool {
		return len(spec.JobTypes) == 2 && spec.JobTypes[0] == "monitor:poll" && spec.JobTypes[1] == "monitor:downsample"
	})
}

func TestFakeFindNotFound(t *testing.T) {
	f := bus.NewFake()
	_, err := f.FindChain(context.Background(), "missing")
	if !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for chain lookup, got %v", err)
	}
	_, err = f.FindBatch(context.Background(), "missing")
	if !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for batch lookup, got %v", err)
	}
}

func TestFakePruneNoop(t *testing.T) {
	f := bus.NewFake()
	if err := f.Prune(context.Background(), time.Now()); err != nil {
		t.Fatalf("expected fake prune noop, got %v", err)
	}
}

func TestFakeRuntimeNoopAndFluentBuilders(t *testing.T) {
	f := bus.NewFake()

	// No-op runtime methods should be callable.
	f.Register("monitor:noop", func(context.Context, bus.Context) error { return nil })
	if err := f.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers noop failed: %v", err)
	}
	if err := f.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown noop failed: %v", err)
	}

	// Chain fluent methods should be callable and keep chain dispatch functional.
	chainID, err := f.Chain(bus.NewJob("a", nil)).
		OnQueue("critical").
		Catch(func(context.Context, bus.ChainState, error) error { return nil }).
		Finally(func(context.Context, bus.ChainState) error { return nil }).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("chain dispatch failed: %v", err)
	}
	if chainID == "" {
		t.Fatal("expected chain id")
	}

	// Batch fluent methods should be callable and keep batch dispatch functional.
	batchID, err := f.Batch(bus.NewJob("a", nil)).
		Name("nightly").
		OnQueue("critical").
		AllowFailures().
		Progress(func(context.Context, bus.BatchState) error { return nil }).
		Then(func(context.Context, bus.BatchState) error { return nil }).
		Catch(func(context.Context, bus.BatchState, error) error { return nil }).
		Finally(func(context.Context, bus.BatchState) error { return nil }).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("batch dispatch failed: %v", err)
	}
	if batchID == "" {
		t.Fatal("expected batch id")
	}
}
