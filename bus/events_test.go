package bus

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue/busruntime"
)

type failingDispatchQueue struct {
	err       error
	handlers  map[string]busruntime.Handler
	workerCnt int
}

func (q *failingDispatchQueue) StartWorkers(context.Context) error { return nil }
func (q *failingDispatchQueue) Shutdown(context.Context) error     { return nil }

func (q *failingDispatchQueue) BusRegister(jobType string, handler busruntime.Handler) {
	if q.handlers == nil {
		q.handlers = make(map[string]busruntime.Handler)
	}
	q.handlers[jobType] = handler
}

func (q *failingDispatchQueue) BusDispatch(context.Context, string, []byte, busruntime.JobOptions) error {
	return q.err
}

func TestDispatchEnqueueFailureEmitsStartedThenFailed(t *testing.T) {
	q := &failingDispatchQueue{err: errors.New("enqueue failed")}
	var kinds []EventKind
	b, err := NewWithStore(q, NewMemoryStore(), WithObserver(ObserverFunc(func(_ context.Context, e Event) {
		kinds = append(kinds, e.Kind)
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	res, err := b.Dispatch(context.Background(), NewJob("monitor:poll", nil))
	if err == nil {
		t.Fatal("expected dispatch enqueue failure")
	}
	if res.DispatchID == "" {
		t.Fatal("expected non-empty dispatch id on enqueue failure")
	}
	if len(kinds) != 2 {
		t.Fatalf("expected 2 events, got %d (%v)", len(kinds), kinds)
	}
	if kinds[0] != EventDispatchStarted || kinds[1] != EventDispatchFailed {
		t.Fatalf("expected started then failed, got %v", kinds)
	}
}

func TestUnknownCallbackKindEmitsCallbackFailed(t *testing.T) {
	q := newSyncTestRuntime()
	var started int
	var failed int
	b, err := New(q, WithObserver(ObserverFunc(func(_ context.Context, e Event) {
		if e.Kind == EventCallbackStarted {
			started++
		}
		if e.Kind == EventCallbackFailed {
			failed++
		}
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	payload := map[string]any{
		"schema_version": 1,
		"dispatch_id":    "d1",
		"kind":           "callback",
		"job_id":         "j1",
		"callback_kind":  "unknown_kind",
	}
	if err := q.DispatchJSON(context.Background(), internalJobCallback, payload); err == nil {
		t.Fatal("expected unknown callback kind error")
	}
	if started != 1 {
		t.Fatalf("expected callback started once, got %d", started)
	}
	if failed != 1 {
		t.Fatalf("expected callback failed once, got %d", failed)
	}
}

func TestCallbackMissingRequiredIDsEmitsCallbackFailed(t *testing.T) {
	q := newSyncTestRuntime()
	var failed int
	b, err := New(q, WithObserver(ObserverFunc(func(_ context.Context, e Event) {
		if e.Kind == EventCallbackFailed {
			failed++
		}
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	tests := []map[string]any{
		{
			"schema_version": 1,
			"dispatch_id":    "d1",
			"kind":           "callback",
			"job_id":         "j1",
			"callback_kind":  "chain_catch",
			// missing chain_id
		},
		{
			"schema_version": 1,
			"dispatch_id":    "d2",
			"kind":           "callback",
			"job_id":         "j2",
			"callback_kind":  "batch_then",
			// missing batch_id
		},
	}

	for i, payloadMap := range tests {
		if err := q.DispatchJSON(context.Background(), internalJobCallback, payloadMap); err == nil {
			t.Fatalf("expected callback validation error for case %d", i)
		}
	}

	if failed != len(tests) {
		t.Fatalf("expected %d callback failed events, got %d", len(tests), failed)
	}
}

func TestMultiObserverPanicsAreIsolated(t *testing.T) {
	var called int
	observer := MultiObserver(
		ObserverFunc(func(context.Context, Event) { panic("boom") }),
		ObserverFunc(func(context.Context, Event) { called++ }),
	)
	observer.Observe(context.Background(), Event{Kind: EventDispatchStarted})
	if called != 1 {
		t.Fatalf("expected second observer called once despite panic, got %d", called)
	}
}

func TestChainEnqueueFailureInvokesCatchAndFinally(t *testing.T) {
	q := &failingDispatchQueue{err: errors.New("enqueue failed")}
	bi, err := NewWithStore(q, NewMemoryStore())
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	b := bi.(*runtime)

	var catchCount int
	var finallyCount int
	chainID, err := b.Chain(NewJob("monitor:poll", nil)).
		Catch(func(context.Context, ChainState, error) error {
			catchCount++
			return nil
		}).
		Finally(func(context.Context, ChainState) error {
			finallyCount++
			return nil
		}).
		Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected chain enqueue error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once, got %d", catchCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}
	st, err := b.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find failed chain: %v", err)
	}
	if !st.Failed {
		t.Fatalf("expected chain marked failed, got %+v", st)
	}
	b.mu.RLock()
	cbCount := len(b.chainCallbacks)
	b.mu.RUnlock()
	if cbCount != 0 {
		t.Fatalf("expected chain callbacks cleaned, got %d", cbCount)
	}
}

func TestBatchEnqueueFailureInvokesCatchAndFinally(t *testing.T) {
	q := &failingDispatchQueue{err: errors.New("enqueue failed")}
	bi, err := NewWithStore(q, NewMemoryStore())
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	b := bi.(*runtime)

	var catchCount int
	var finallyCount int
	batchID, err := b.Batch(NewJob("monitor:poll", nil)).
		Catch(func(context.Context, BatchState, error) error {
			catchCount++
			return nil
		}).
		Finally(func(context.Context, BatchState) error {
			finallyCount++
			return nil
		}).
		Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected batch enqueue error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once, got %d", catchCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}
	st, err := b.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find failed batch: %v", err)
	}
	if !st.Completed || !st.Cancelled {
		t.Fatalf("expected batch cancelled+completed, got %+v", st)
	}
	b.mu.RLock()
	cbCount := len(b.batchCallbacks)
	b.mu.RUnlock()
	if cbCount != 0 {
		t.Fatalf("expected batch callbacks cleaned, got %d", cbCount)
	}
}

func TestChainDispatchFailureStillReturnsChainID(t *testing.T) {
	q := newSyncTestRuntime()
	b, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:downsample", func(context.Context, Context) error { return errors.New("boom") })

	chainID, err := b.Chain(NewJob("monitor:downsample", nil)).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected chain dispatch error")
	}
	if chainID == "" {
		t.Fatal("expected non-empty chain id on dispatch error")
	}
}

func TestBatchDispatchFailureStillReturnsBatchID(t *testing.T) {
	q := newSyncTestRuntime()
	b, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:downsample", func(context.Context, Context) error { return errors.New("boom") })

	batchID, err := b.Batch(NewJob("monitor:downsample", nil)).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected batch dispatch error")
	}
	if batchID == "" {
		t.Fatal("expected non-empty batch id on dispatch error")
	}
}
