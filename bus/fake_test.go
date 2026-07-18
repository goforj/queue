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

// fakeDeferredPayload proves legacy builders retain Dispatch-time JSON encoding.
type fakeDeferredPayload struct {
	Value int `json:"value"`
}

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

// TestFakeZeroValueSharesOneConcurrentRecorder preserves the historical usable
// zero value without allowing concurrent callers to initialize separate state.
func TestFakeZeroValueSharesOneConcurrentRecorder(t *testing.T) {
	var fake bus.Fake
	queues := make(chan *queue.FakeQueue, 16)
	var wait sync.WaitGroup
	for i := 0; i < cap(queues); i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			queues <- fake.Queue()
		}()
	}
	wait.Wait()
	close(queues)
	var first *queue.FakeQueue
	for candidate := range queues {
		if first == nil {
			first = candidate
			continue
		}
		if candidate != first {
			t.Fatal("zero-value Fake initialized more than one canonical queue")
		}
	}
	copied := fake
	if copied.Queue() != fake.Queue() {
		t.Fatal("copy of initialized Fake lost canonical queue identity")
	}
	if _, err := fake.Dispatch(context.Background(), bus.NewJob("zero:direct", nil)); err != nil {
		t.Fatalf("zero-value direct dispatch: %v", err)
	}
	if _, err := fake.Chain(bus.NewJob("zero:chain", nil)).Dispatch(context.Background()); err != nil {
		t.Fatalf("zero-value chain dispatch: %v", err)
	}
	if _, err := fake.Batch(bus.NewJob("zero:batch", nil)).Dispatch(context.Background()); err != nil {
		t.Fatalf("zero-value batch dispatch: %v", err)
	}
	fake.AssertDispatched(t, "zero:direct")
	fake.AssertChained(t, []string{"zero:chain"})
	fake.AssertBatchCount(t, 1)
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

// TestFakeNilReceiverLifecycleCompatibility preserves the historical inert
// lifecycle and missing-state behavior on a nil legacy fake pointer.
func TestFakeNilReceiverLifecycleCompatibility(t *testing.T) {
	var fake *bus.Fake
	if err := fake.StartWorkers(context.Background()); err != nil {
		t.Fatalf("nil StartWorkers: %v", err)
	}
	if err := fake.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
	if err := fake.Prune(context.Background(), time.Now()); err != nil {
		t.Fatalf("nil Prune: %v", err)
	}
	if _, err := fake.FindChain(context.Background(), "missing"); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("nil FindChain error = %v, want ErrNotFound", err)
	}
	if _, err := fake.FindBatch(context.Background(), "missing"); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("nil FindBatch error = %v, want ErrNotFound", err)
	}
}

// TestFakePrunePreservesActiveWorkflow verifies the compatibility method uses
// canonical retention semantics rather than an unrelated fake no-op.
func TestFakePrunePreservesActiveWorkflow(t *testing.T) {
	f := bus.NewFake()
	chainID, err := f.Chain(bus.NewJob("prune:active", nil)).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch active chain: %v", err)
	}
	if err := f.Prune(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("prune active fake state: %v", err)
	}
	if _, err := f.FindChain(context.Background(), chainID); err != nil {
		t.Fatalf("active chain was pruned: %v", err)
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

// TestFakeSharesCanonicalRootState verifies the legacy surface is only a typed
// conversion and assertion view over queue.FakeQueue.
func TestFakeSharesCanonicalRootState(t *testing.T) {
	fake := bus.NewFake()
	root := fake.Queue()
	if root == nil {
		t.Fatal("Queue returned nil canonical fake")
	}
	if err := root.Dispatch(queue.NewJob("root:dispatch").OnQueue("root")); err != nil {
		t.Fatalf("root dispatch: %v", err)
	}
	result, err := fake.Dispatch(context.Background(), bus.NewJob("bus:dispatch", nil).OnQueue("legacy"))
	if err != nil {
		t.Fatalf("bus dispatch: %v", err)
	}
	if result.DispatchID != "fake" {
		t.Fatalf("legacy direct fake ID = %q, want fake", result.DispatchID)
	}

	fake.AssertDispatched(t, "root:dispatch")
	root.AssertDispatched(t, "bus:dispatch")
	if got := len(root.Records()); got != 2 {
		t.Fatalf("shared direct records = %d, want 2", got)
	}
}

// TestFakeBuildersUseProductionTimingAndOptions verifies deferred legacy
// encoding and fluent workflow policy survive the compatibility adapter.
func TestFakeBuildersUseProductionTimingAndOptions(t *testing.T) {
	fake := bus.NewFake()
	payload := &fakeDeferredPayload{Value: 1}
	chainBuilder := fake.Chain(
		bus.NewJob("chain:first", payload),
		bus.NewJob("chain:second", nil).OnQueue("dedicated"),
	).OnQueue("chain-default")
	batchBuilder := fake.Batch(
		bus.NewJob("batch:first", payload),
		bus.NewJob("batch:second", nil).OnQueue("priority"),
	).Name("compatibility batch").OnQueue("batch-default").AllowFailures()
	if len(fake.Queue().ChainRecords()) != 0 || len(fake.Queue().BatchRecords()) != 0 {
		t.Fatal("builder construction recorded workflow state")
	}
	payload.Value = 2

	chainID, err := chainBuilder.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	batchID, err := batchBuilder.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	chains := fake.Queue().ChainRecords()
	if len(chains) != 1 || chains[0].ChainID != chainID || chains[0].Queue != "chain-default" {
		t.Fatalf("chain record = %+v", chains)
	}
	if got := string(chains[0].Nodes[0].Job.Payload); got != `{"value":2}` {
		t.Fatalf("deferred chain payload = %q", got)
	}
	if chains[0].Nodes[0].Job.Options.Queue != "chain-default" || chains[0].Nodes[1].Job.Options.Queue != "dedicated" {
		t.Fatalf("chain queue precedence = %+v", chains[0].Nodes)
	}
	batches := fake.Queue().BatchRecords()
	if len(batches) != 1 || batches[0].BatchID != batchID || batches[0].Name != "compatibility batch" || !batches[0].AllowFailed {
		t.Fatalf("batch record = %+v", batches)
	}
	if batches[0].Jobs[0].Job.Options.Queue != "batch-default" || batches[0].Jobs[1].Job.Options.Queue != "priority" {
		t.Fatalf("batch queue precedence = %+v", batches[0].Jobs)
	}
	if _, err := fake.FindChain(context.Background(), chainID); err != nil {
		t.Fatalf("find accepted chain: %v", err)
	}
	if state, err := fake.FindBatch(context.Background(), batchID); err != nil || state.Total != 2 {
		t.Fatalf("find accepted batch = %+v, %v", state, err)
	}
}

// TestFakeRejectedBuildersRemainInvisible verifies validation and context
// failures do not create false-positive legacy assertions.
func TestFakeRejectedBuildersRemainInvisible(t *testing.T) {
	fake := bus.NewFake()
	_ = fake.Chain(bus.NewJob("abandoned", nil))
	_ = fake.Batch(bus.NewJob("abandoned", nil))
	if _, err := fake.Dispatch(context.Background(), bus.NewJob("", nil)); err == nil {
		t.Fatal("empty direct type error = nil")
	}
	if _, err := fake.Dispatch(context.Background(), bus.NewJob("bad:direct-payload", failingJSONPayload{})); err == nil {
		t.Fatal("invalid direct payload error = nil")
	}
	if _, err := fake.Chain().Dispatch(context.Background()); err == nil {
		t.Fatal("empty chain error = nil")
	}
	if _, err := fake.Batch().Dispatch(context.Background()); err == nil {
		t.Fatal("empty batch error = nil")
	}
	if _, err := fake.Chain(bus.NewJob("", nil)).Dispatch(context.Background()); err == nil {
		t.Fatal("empty chain member type error = nil")
	}
	if _, err := fake.Batch(bus.NewJob("bad:retry", nil).Retry(-1)).Dispatch(context.Background()); err == nil {
		t.Fatal("invalid batch retry error = nil")
	}
	if _, err := fake.Chain(bus.NewJob("bad:chain-payload", failingJSONPayload{})).Dispatch(context.Background()); err == nil {
		t.Fatal("invalid chain payload error = nil")
	}
	if _, err := fake.Batch(bus.NewJob("bad:batch-payload", failingJSONPayload{})).Dispatch(context.Background()); err == nil {
		t.Fatal("invalid batch payload error = nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	callbackCalled := false
	if _, err := fake.Dispatch(canceled, bus.NewJob("cancelled:direct", nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled direct error = %v", err)
	}
	if _, err := fake.Chain(bus.NewJob("cancelled:chain", nil)).
		Catch(func(context.Context, bus.ChainState, error) error { callbackCalled = true; return nil }).
		Finally(func(context.Context, bus.ChainState) error { callbackCalled = true; return nil }).
		Dispatch(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled chain error = %v", err)
	}
	if _, err := fake.Batch(bus.NewJob("cancelled:batch", nil)).
		Progress(func(context.Context, bus.BatchState) error { callbackCalled = true; return nil }).
		Then(func(context.Context, bus.BatchState) error { callbackCalled = true; return nil }).
		Catch(func(context.Context, bus.BatchState, error) error { callbackCalled = true; return nil }).
		Finally(func(context.Context, bus.BatchState) error { callbackCalled = true; return nil }).
		Dispatch(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch error = %v", err)
	}
	if callbackCalled {
		t.Fatal("recording fake invoked a workflow callback")
	}
	if len(fake.Queue().Records()) != 0 || len(fake.Queue().ChainRecords()) != 0 || len(fake.Queue().BatchRecords()) != 0 {
		t.Fatalf("rejected records = direct:%d chains:%d batches:%d", len(fake.Queue().Records()), len(fake.Queue().ChainRecords()), len(fake.Queue().BatchRecords()))
	}
}

// TestFakeConcurrentCompatibilityViews exercises legacy conversion and shared
// root inspection under the race detector.
func TestFakeConcurrentCompatibilityViews(t *testing.T) {
	fake := bus.NewFake()
	var wait sync.WaitGroup
	for worker := 0; worker < 9; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 30; iteration++ {
				switch worker % 3 {
				case 0:
					_, _ = fake.Dispatch(context.Background(), bus.NewJob("direct:legacy", map[string]int{"iteration": iteration}))
				case 1:
					_, _ = fake.Chain(bus.NewJob("chain:legacy", iteration)).Dispatch(context.Background())
				case 2:
					_, _ = fake.Batch(bus.NewJob("batch:legacy", iteration)).Dispatch(context.Background())
				}
				_ = fake.Queue().Records()
				_ = fake.Queue().ChainRecords()
				_ = fake.Queue().BatchRecords()
			}
		}()
	}
	wait.Wait()
	if got := len(fake.Queue().Records()); got != 90 {
		t.Fatalf("concurrent direct records = %d, want 90", got)
	}
	if got := len(fake.Queue().ChainRecords()); got != 90 {
		t.Fatalf("concurrent chain records = %d, want 90", got)
	}
	if got := len(fake.Queue().BatchRecords()); got != 90 {
		t.Fatalf("concurrent batch records = %d, want 90", got)
	}
}
