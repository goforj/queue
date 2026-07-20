package queue_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/goforj/queue"
)

// retryEventRecorder keeps public retry assertions safe if a backend invokes observers concurrently.
type retryEventRecorder struct {
	mu     sync.Mutex
	events []queue.Event
}

// Observe records one unified event.
func (r *retryEventRecorder) Observe(_ context.Context, event queue.Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

// snapshot returns an isolated copy so assertions cannot race an observer call.
func (r *retryEventRecorder) snapshot() []queue.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]queue.Event(nil), r.events...)
}

// TestPublicChainWaitsForTerminalAttempt verifies transient delivery failures cannot terminally fail a workflow.
func TestPublicChainWaitsForTerminalAttempt(t *testing.T) {
	recorder := &retryEventRecorder{}
	q, err := queue.NewSync(queue.WithObserver(recorder))
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	var attempts []int
	q.Register("contract:chain:retry", func(_ context.Context, message queue.Message) error {
		attempts = append(attempts, message.Attempt)
		if message.Attempt == 0 {
			return errors.New("transient")
		}
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	catchCalls := 0
	finallyCalls := 0
	chainID, err := q.Chain(queue.NewJob("contract:chain:retry").Retry(1)).
		Catch(func(context.Context, queue.ChainState, error) error {
			catchCalls++
			return nil
		}).
		Finally(func(context.Context, queue.ChainState) error {
			finallyCalls++
			return nil
		}).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch retrying chain: %v", err)
	}
	state, err := q.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !state.Completed || state.Failed || catchCalls != 0 || finallyCalls != 1 {
		t.Fatalf("chain state/callbacks = %+v catch:%d finally:%d", state, catchCalls, finallyCalls)
	}
	assertRetryAttempts(t, attempts)
	assertTransientWorkflowEvents(t, recorder.snapshot(), queue.EventChainFailed)
}

// TestPublicBatchWaitsForTerminalAttempt verifies a retrying batch item is counted only after its final outcome.
func TestPublicBatchWaitsForTerminalAttempt(t *testing.T) {
	recorder := &retryEventRecorder{}
	q, err := queue.NewSync(queue.WithObserver(recorder))
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	var attempts []int
	q.Register("contract:batch:retry", func(_ context.Context, message queue.Message) error {
		attempts = append(attempts, message.Attempt)
		if message.Attempt == 0 {
			return errors.New("transient")
		}
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	catchCalls := 0
	finallyCalls := 0
	batchID, err := q.Batch(queue.NewJob("contract:batch:retry").Retry(1)).
		Catch(func(context.Context, queue.BatchState, error) error {
			catchCalls++
			return nil
		}).
		Finally(func(context.Context, queue.BatchState) error {
			finallyCalls++
			return nil
		}).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch retrying batch: %v", err)
	}
	state, err := q.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if !state.Completed || state.Cancelled || state.Failed != 0 || state.Processed != 1 || catchCalls != 0 || finallyCalls != 1 {
		t.Fatalf("batch state/callbacks = %+v catch:%d finally:%d", state, catchCalls, finallyCalls)
	}
	assertRetryAttempts(t, attempts)
	assertTransientWorkflowEvents(t, recorder.snapshot(), queue.EventBatchFailed)
}

// TestPublicChainFailsOnlyAfterRetryExhaustion verifies terminal state and callbacks commit exactly once.
func TestPublicChainFailsOnlyAfterRetryExhaustion(t *testing.T) {
	recorder := &retryEventRecorder{}
	q, err := queue.NewSync(queue.WithObserver(recorder))
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	var attempts []int
	q.Register("contract:chain:exhaust", func(_ context.Context, message queue.Message) error {
		attempts = append(attempts, message.Attempt)
		return errors.New("terminal")
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	catchCalls := 0
	finallyCalls := 0
	chainID, dispatchErr := q.Chain(queue.NewJob("contract:chain:exhaust").Retry(1)).
		Catch(func(context.Context, queue.ChainState, error) error {
			catchCalls++
			return nil
		}).
		Finally(func(context.Context, queue.ChainState) error {
			finallyCalls++
			return nil
		}).
		Dispatch(context.Background())
	if dispatchErr == nil {
		t.Fatal("exhausted chain dispatch must return the handler error")
	}
	state, err := q.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !state.Failed || state.Completed || catchCalls != 1 || finallyCalls != 1 {
		t.Fatalf("chain state/callbacks = %+v catch:%d finally:%d", state, catchCalls, finallyCalls)
	}
	assertRetryAttempts(t, attempts)
	events := recorder.snapshot()
	if countRetryEvents(events, queue.EventJobFailed) != 1 || countRetryEvents(events, queue.EventChainFailed) != 1 {
		t.Fatalf("terminal events must occur once: %+v", events)
	}
}

// TestPublicChainDownstreamFailureDoesNotRetryPredecessor verifies a synchronous
// downstream failure cannot consume the retry budget of an already committed node.
func TestPublicChainDownstreamFailureDoesNotRetryPredecessor(t *testing.T) {
	recorder := &retryEventRecorder{}
	q, err := queue.NewSync(queue.WithObserver(recorder))
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}

	predecessorCalls := 0
	q.Register("contract:chain:predecessor", func(context.Context, queue.Message) error {
		predecessorCalls++
		return nil
	})
	downstreamErr := errors.New("downstream terminal failure")
	downstreamCalls := 0
	q.Register("contract:chain:downstream", func(context.Context, queue.Message) error {
		downstreamCalls++
		return downstreamErr
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	chainID, dispatchErr := q.Chain(
		queue.NewJob("contract:chain:predecessor").Retry(1),
		queue.NewJob("contract:chain:downstream").Retry(0),
	).Dispatch(context.Background())
	if !errors.Is(dispatchErr, downstreamErr) {
		t.Fatalf("dispatch error = %v, want downstream error", dispatchErr)
	}
	if predecessorCalls != 1 || downstreamCalls != 1 {
		t.Fatalf("handler calls = predecessor:%d downstream:%d, want 1 each", predecessorCalls, downstreamCalls)
	}

	state, err := q.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !state.Failed || state.Completed {
		t.Fatalf("chain state = %+v, want failed and never completed", state)
	}
	events := recorder.snapshot()
	if countRetryEvents(events, queue.EventChainFailed) != 1 || countRetryEvents(events, queue.EventChainCompleted) != 0 {
		t.Fatalf("terminal chain events are inconsistent: %+v", events)
	}
	if countRetryEvents(events, queue.EventChainAdvanced) != 1 {
		t.Fatalf("chain advance count is not one: %+v", events)
	}
	if countRetryJobEvents(events, queue.EventJobSucceeded, "contract:chain:predecessor") != 1 ||
		countRetryJobEvents(events, queue.EventJobFailed, "contract:chain:predecessor") != 0 ||
		countRetryJobEvents(events, queue.EventJobSucceeded, "contract:chain:downstream") != 0 ||
		countRetryJobEvents(events, queue.EventJobFailed, "contract:chain:downstream") != 1 {
		t.Fatalf("job outcome events are inconsistent: %+v", events)
	}
}

// assertRetryAttempts verifies messages expose the physical zero-based attempt sequence.
func assertRetryAttempts(t *testing.T, attempts []int) {
	t.Helper()
	if len(attempts) != 2 || attempts[0] != 0 || attempts[1] != 1 {
		t.Fatalf("attempts = %v, want [0 1]", attempts)
	}
}

// assertTransientWorkflowEvents verifies a transient failure stays below the terminal workflow boundary.
func assertTransientWorkflowEvents(t *testing.T, events []queue.Event, terminalKind queue.EventKind) {
	t.Helper()
	if countRetryEvents(events, queue.EventProcessFailed) != 1 {
		t.Fatalf("process failure count is not one: %+v", events)
	}
	if countRetryEvents(events, queue.EventJobFailed) != 0 || countRetryEvents(events, terminalKind) != 0 {
		t.Fatalf("transient attempt emitted terminal workflow facts: %+v", events)
	}
	jobAttempts := make([]int, 0, 2)
	for _, event := range events {
		if event.Kind == queue.EventJobStarted {
			jobAttempts = append(jobAttempts, event.Attempt)
		}
	}
	assertRetryAttempts(t, jobAttempts)
}

// countRetryEvents returns the number of matching unified facts.
func countRetryEvents(events []queue.Event, kind queue.EventKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

// countRetryJobEvents returns the number of matching facts for one logical job type.
func countRetryJobEvents(events []queue.Event, kind queue.EventKind, jobType string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind && event.JobType == jobType {
			count++
		}
	}
	return count
}
