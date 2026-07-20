package bus_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

type facadeDeferredPayload struct {
	calls *atomic.Int32
	value int
}

// MarshalJSON records when the compatibility facade freezes the referenced payload state.
func (p *facadeDeferredPayload) MarshalJSON() ([]byte, error) {
	p.calls.Add(1)
	return json.Marshal(struct {
		Value int `json:"value"`
	}{Value: p.value})
}

// TestQueueFacadeForwardsFluentBuildersCallbacksAndPrune exercises the retained facade through one shared root engine.
func TestQueueFacadeForwardsFluentBuildersCallbacksAndPrune(t *testing.T) {
	root, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	compatibility, err := bus.New(root)
	if err != nil {
		t.Fatalf("new compatibility facade: %v", err)
	}
	if err := compatibility.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start compatibility workers: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := compatibility.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown compatibility workers: %v", shutdownErr)
		}
	})

	cause := errors.New("compatibility handler failed")
	compatibility.Register("compat:facade-fail", func(context.Context, bus.Context) error {
		return cause
	})

	var chainCatch atomic.Int32
	var chainFinally atomic.Int32
	chainID, err := compatibility.Chain(bus.NewJob("compat:facade-fail", nil)).
		OnQueue("critical").
		Catch(func(_ context.Context, state bus.ChainState, callbackErr error) error {
			if state.Queue != "critical" || callbackErr == nil || callbackErr.Error() != cause.Error() {
				t.Errorf("chain catch state/error = %+v/%v", state, callbackErr)
			}
			chainCatch.Add(1)
			return nil
		}).
		Finally(func(_ context.Context, state bus.ChainState) error {
			if state.Queue != "critical" || !state.Failed {
				t.Errorf("chain finally state = %+v", state)
			}
			chainFinally.Add(1)
			return nil
		}).
		Dispatch(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("chain error = %v, want handler cause", err)
	}
	if chainCatch.Load() != 1 || chainFinally.Load() != 1 {
		t.Fatalf("chain callback counts = %d/%d, want 1/1", chainCatch.Load(), chainFinally.Load())
	}
	chainState, err := compatibility.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if len(chainState.Nodes) != 1 || chainState.Nodes[0].Job.Options.Queue != "critical" {
		t.Fatalf("chain routing state = %+v", chainState)
	}

	var batchProgress atomic.Int32
	var batchThen atomic.Int32
	var batchCatch atomic.Int32
	var batchFinally atomic.Int32
	batchID, err := compatibility.Batch(bus.NewJob("compat:facade-fail", nil)).
		Name("compatibility batch").
		OnQueue("bulk").
		AllowFailures().
		Progress(func(_ context.Context, state bus.BatchState) error {
			if state.Name != "compatibility batch" || state.Queue != "bulk" {
				t.Errorf("batch progress state = %+v", state)
			}
			batchProgress.Add(1)
			return nil
		}).
		Then(func(_ context.Context, state bus.BatchState) error {
			if !state.Completed || state.Cancelled {
				t.Errorf("batch then state = %+v", state)
			}
			batchThen.Add(1)
			return nil
		}).
		Catch(func(_ context.Context, state bus.BatchState, callbackErr error) error {
			if state.Failed != 1 || callbackErr == nil || callbackErr.Error() != cause.Error() {
				t.Errorf("batch catch state/error = %+v/%v", state, callbackErr)
			}
			batchCatch.Add(1)
			return nil
		}).
		Finally(func(_ context.Context, state bus.BatchState) error {
			if !state.Completed || state.Queue != "bulk" {
				t.Errorf("batch finally state = %+v", state)
			}
			batchFinally.Add(1)
			return nil
		}).
		Dispatch(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("batch error = %v, want handler cause", err)
	}
	if batchProgress.Load() != 1 || batchThen.Load() != 1 || batchCatch.Load() != 1 || batchFinally.Load() != 1 {
		t.Fatalf("batch callback counts = %d/%d/%d/%d, want 1/1/1/1", batchProgress.Load(), batchThen.Load(), batchCatch.Load(), batchFinally.Load())
	}
	batchState, err := compatibility.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if batchState.Name != "compatibility batch" || batchState.Queue != "bulk" || !batchState.AllowFailed || !batchState.Completed || batchState.Cancelled {
		t.Fatalf("batch state = %+v", batchState)
	}

	if err := compatibility.Prune(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("prune compatibility state: %v", err)
	}
	if _, err := compatibility.FindChain(context.Background(), chainID); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("find pruned chain error = %v, want ErrNotFound", err)
	}
	if _, err := compatibility.FindBatch(context.Background(), batchID); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("find pruned batch error = %v, want ErrNotFound", err)
	}
}

// TestQueueFacadeDefersLegacyConversionFailuresAcrossFluentBuilders verifies errors remain at the legacy Dispatch boundary.
func TestQueueFacadeDefersLegacyConversionFailuresAcrossFluentBuilders(t *testing.T) {
	root, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := root.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown root queue: %v", shutdownErr)
		}
	})
	compatibility, err := bus.New(root)
	if err != nil {
		t.Fatalf("new compatibility facade: %v", err)
	}

	chainCallbackCalled := false
	_, err = compatibility.Chain(bus.Job{}).
		OnQueue("critical").
		Catch(func(context.Context, bus.ChainState, error) error {
			chainCallbackCalled = true
			return nil
		}).
		Finally(func(context.Context, bus.ChainState) error {
			chainCallbackCalled = true
			return nil
		}).
		Dispatch(context.Background())
	if err == nil || err.Error() != "bus job type is required" {
		t.Fatalf("chain conversion error = %v, want missing legacy type", err)
	}
	if chainCallbackCalled {
		t.Fatal("deferred chain conversion failure invoked callbacks")
	}

	batchCallbackCalled := false
	_, err = compatibility.Batch(bus.NewJob("compat:marshal-failure", failingJSONPayload{})).
		Name("unreachable").
		OnQueue("bulk").
		AllowFailures().
		Progress(func(context.Context, bus.BatchState) error {
			batchCallbackCalled = true
			return nil
		}).
		Then(func(context.Context, bus.BatchState) error {
			batchCallbackCalled = true
			return nil
		}).
		Catch(func(context.Context, bus.BatchState, error) error {
			batchCallbackCalled = true
			return nil
		}).
		Finally(func(context.Context, bus.BatchState) error {
			batchCallbackCalled = true
			return nil
		}).
		Dispatch(context.Background())
	if err == nil || err.Error() != "json: error calling MarshalJSON for type bus_test.failingJSONPayload: compat marshal failure" {
		t.Fatalf("batch conversion error = %v, want legacy marshal failure", err)
	}
	if batchCallbackCalled {
		t.Fatal("deferred batch conversion failure invoked callbacks")
	}
}

// TestQueueFacadeDefersBuilderEncodingAndKeepsShallowJobSnapshots freezes the legacy builder timing contract.
func TestQueueFacadeDefersBuilderEncodingAndKeepsShallowJobSnapshots(t *testing.T) {
	root, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	compatibility, err := bus.New(root)
	if err != nil {
		t.Fatalf("new compatibility facade: %v", err)
	}
	if err := compatibility.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start compatibility workers: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := compatibility.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown compatibility workers: %v", shutdownErr)
		}
	})

	var chainSeen atomic.Int32
	compatibility.Register("compat:deferred-chain", func(_ context.Context, message bus.Context) error {
		var payload struct {
			Value int `json:"value"`
		}
		if bindErr := message.Bind(&payload); bindErr != nil {
			return bindErr
		}
		chainSeen.Store(int32(payload.Value))
		return nil
	})
	var batchSeen atomic.Int32
	compatibility.Register("compat:deferred-batch", func(_ context.Context, message bus.Context) error {
		var payload struct {
			Value int `json:"value"`
		}
		if bindErr := message.Bind(&payload); bindErr != nil {
			return bindErr
		}
		batchSeen.Store(int32(payload.Value))
		return nil
	})

	var chainMarshalCalls atomic.Int32
	chainPayload := &facadeDeferredPayload{calls: &chainMarshalCalls, value: 1}
	chainJobs := []bus.Job{bus.NewJob("compat:deferred-chain", chainPayload).OnQueue("chain-job")}
	chainBuilder := compatibility.Chain(chainJobs...).OnQueue("chain-default").Catch(nil).Finally(nil)
	if chainMarshalCalls.Load() != 0 {
		t.Fatalf("chain marshal calls before Dispatch = %d, want 0", chainMarshalCalls.Load())
	}
	chainJobs[0].Type = "compat:mutated-chain"
	chainJobs[0].Options.Queue = "mutated-chain-job"
	chainPayload.value = 2
	chainID, err := chainBuilder.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch deferred chain: %v", err)
	}
	if chainMarshalCalls.Load() != 1 || chainSeen.Load() != 2 {
		t.Fatalf("chain marshal calls/payload = %d/%d, want 1/2", chainMarshalCalls.Load(), chainSeen.Load())
	}
	chainState, err := compatibility.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find deferred chain: %v", err)
	}
	if len(chainState.Nodes) != 1 || chainState.Nodes[0].Job.Type != "compat:deferred-chain" || chainState.Nodes[0].Job.Options.Queue != "chain-job" {
		t.Fatalf("chain shallow snapshot = %+v", chainState)
	}

	var batchMarshalCalls atomic.Int32
	batchPayload := &facadeDeferredPayload{calls: &batchMarshalCalls, value: 3}
	batchJobs := []bus.Job{bus.NewJob("compat:deferred-batch", batchPayload).OnQueue("batch-job")}
	batchBuilder := compatibility.Batch(batchJobs...).Name("deferred batch").OnQueue("batch-default").AllowFailures().
		Progress(nil).Then(nil).Catch(nil).Finally(nil)
	if batchMarshalCalls.Load() != 0 {
		t.Fatalf("batch marshal calls before Dispatch = %d, want 0", batchMarshalCalls.Load())
	}
	batchJobs[0].Type = "compat:mutated-batch"
	batchJobs[0].Options.Queue = "mutated-batch-job"
	batchPayload.value = 4
	batchID, err := batchBuilder.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch deferred batch: %v", err)
	}
	if batchMarshalCalls.Load() != 1 || batchSeen.Load() != 4 {
		t.Fatalf("batch marshal calls/payload = %d/%d, want 1/4", batchMarshalCalls.Load(), batchSeen.Load())
	}
	batchState, err := compatibility.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find deferred batch: %v", err)
	}
	if batchState.Name != "deferred batch" || batchState.Queue != "batch-default" || !batchState.AllowFailed || !batchState.Completed {
		t.Fatalf("deferred batch state = %+v", batchState)
	}
}

// TestRawRuntimeFacadeForwardsNilCallbacksAndShutdown covers the retained low-level compatibility lifecycle.
func TestRawRuntimeFacadeForwardsNilCallbacksAndShutdown(t *testing.T) {
	runtime, err := newBusTestRuntime(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		t.Fatalf("new raw sync runtime: %v", err)
	}
	compatibility, err := bus.New(runtime)
	if err != nil {
		t.Fatalf("new raw compatibility facade: %v", err)
	}
	t.Cleanup(func() {
		_ = compatibility.Shutdown(context.Background())
	})
	compatibility.Register("compat:nil-handler", nil)
	compatibility.Register("compat:raw-success", func(context.Context, bus.Context) error { return nil })
	if err := compatibility.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start raw compatibility workers: %v", err)
	}
	if _, err := compatibility.Dispatch(context.Background(), bus.NewJob("compat:nil-handler", nil)); err == nil {
		t.Fatal("nil compatibility registration accepted a delivery")
	} else if !strings.Contains(err.Error(), "handler not registered") {
		t.Fatalf("nil compatibility dispatch error = %v, want missing handler", err)
	}

	chainID, err := compatibility.Chain(bus.NewJob("compat:raw-success", nil)).
		OnQueue("raw-chain").
		Catch(nil).
		Finally(nil).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch raw chain: %v", err)
	}
	chainState, err := compatibility.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find raw chain: %v", err)
	}
	if chainState.Queue != "raw-chain" || !chainState.Completed {
		t.Fatalf("raw chain state = %+v", chainState)
	}

	batchID, err := compatibility.Batch(bus.NewJob("compat:raw-success", nil)).
		Name("raw batch").
		OnQueue("raw-batch").
		AllowFailures().
		Progress(nil).
		Then(nil).
		Catch(nil).
		Finally(nil).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch raw batch: %v", err)
	}
	batchState, err := compatibility.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find raw batch: %v", err)
	}
	if batchState.Name != "raw batch" || batchState.Queue != "raw-batch" || !batchState.AllowFailed || !batchState.Completed {
		t.Fatalf("raw batch state = %+v", batchState)
	}

	if err := compatibility.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown raw compatibility workers: %v", err)
	}
}
