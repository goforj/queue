package workflow

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
	if started != 0 {
		t.Fatalf("invalid callback emitted %d started events, want 0", started)
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

// TestCallbackFunctionErrorEmitsFailed verifies an invoked ephemeral callback cannot be reported as successful.
func TestCallbackFunctionErrorEmitsFailed(t *testing.T) {
	tests := []struct {
		name       string
		handlerErr error
		dispatch   func(Engine, error)
	}{
		{
			name:       "chain catch",
			handlerErr: errors.New("handler failed"),
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Chain(NewJob("job:callback-error", nil)).
					Catch(func(context.Context, ChainState, error) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name: "chain finally",
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Chain(NewJob("job:callback-error", nil)).
					Finally(func(context.Context, ChainState) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name:       "batch catch",
			handlerErr: errors.New("handler failed"),
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Batch(NewJob("job:callback-error", nil)).
					Catch(func(context.Context, BatchState, error) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name: "batch then",
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Batch(NewJob("job:callback-error", nil)).
					Then(func(context.Context, BatchState) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name: "batch finally",
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Batch(NewJob("job:callback-error", nil)).
					Finally(func(context.Context, BatchState) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := newSyncTestRuntime()
			callbackErr := errors.New("callback failed")
			var (
				failed    []Event
				succeeded []Event
			)
			b, err := New(q, WithObserver(ObserverFunc(func(_ context.Context, event Event) {
				switch event.Kind {
				case EventCallbackFailed:
					failed = append(failed, event)
				case EventCallbackSucceeded:
					succeeded = append(succeeded, event)
				}
			})))
			if err != nil {
				t.Fatalf("new bus: %v", err)
			}
			b.Register("job:callback-error", func(context.Context, Context) error { return test.handlerErr })
			if err := b.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start workers: %v", err)
			}
			test.dispatch(b, callbackErr)
			if len(failed) != 1 || !errors.Is(failed[0].Err, callbackErr) {
				t.Fatalf("callback failed events = %#v, want callback cause", failed)
			}
			for _, event := range succeeded {
				if event.JobID == failed[0].JobID {
					t.Fatalf("failed callback job %q later emitted success", event.JobID)
				}
			}
		})
	}
}

// TestCallbackPanicEmitsFailed verifies callback recovery preserves a terminal lifecycle fact and the panic cause.
func TestCallbackPanicEmitsFailed(t *testing.T) {
	queueRuntime := newSyncTestRuntime()
	panicErr := errors.New("callback panic")
	var started int
	var failed []Event
	var succeeded int
	busRuntime, err := New(queueRuntime, WithObserver(ObserverFunc(func(_ context.Context, event Event) {
		switch event.Kind {
		case EventCallbackStarted:
			started++
		case EventCallbackFailed:
			failed = append(failed, event)
		case EventCallbackSucceeded:
			succeeded++
		}
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	busRuntime.Register("job:callback-panic", func(context.Context, Context) error { return nil })
	if err := busRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	if _, err := busRuntime.Batch(NewJob("job:callback-panic", nil)).
		Then(func(context.Context, BatchState) error { panic(panicErr) }).
		Dispatch(context.Background()); err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	if started != 1 || len(failed) != 1 || succeeded != 0 {
		t.Fatalf("callback started/failed/succeeded = %d/%d/%d, want 1/1/0", started, len(failed), succeeded)
	}
	if !errors.Is(failed[0].Err, panicErr) {
		t.Fatalf("callback panic event error = %v, want cause %v", failed[0].Err, panicErr)
	}
}

// TestPositiveWorkflowEventsWaitForDeliverySettlement verifies broker-backed success facts remain pending until acknowledgement.
func TestPositiveWorkflowEventsWaitForDeliverySettlement(t *testing.T) {
	positive := []EventKind{
		EventJobSucceeded,
		EventChainAdvanced,
		EventChainCompleted,
		EventBatchProgressed,
		EventBatchCompleted,
		EventCallbackSucceeded,
	}
	for _, kind := range positive {
		t.Run(string(kind), func(t *testing.T) {
			var events []Event
			runtime := &runtime{observer: ObserverFunc(func(_ context.Context, event Event) {
				events = append(events, event)
			})}
			ctx, settlement := busruntime.WithDeliverySettlement(context.Background())
			runtime.emit(ctx, Event{Kind: kind})
			if len(events) != 0 {
				t.Fatalf("event %q emitted before settlement: %+v", kind, events)
			}
			settlement.Commit()
			if len(events) != 1 || events[0].Kind != kind {
				t.Fatalf("events after settlement = %+v, want %q", events, kind)
			}
		})
	}
}

// TestDuplicateFailedCallbackDoesNotBecomeSuccessful verifies an at-most-once callback marker cannot turn redelivery into a false success.
func TestDuplicateFailedCallbackDoesNotBecomeSuccessful(t *testing.T) {
	const batchID = "batch_callback_failed_duplicate"
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID: batchID,
		Jobs:    []BatchJob{{JobID: "batch_callback_job", Job: StoredJob{Type: "callback:source"}}},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if _, _, err := store.MarkBatchJobSucceeded(context.Background(), batchID, "batch_callback_job"); err != nil {
		t.Fatalf("complete batch: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	callbackErr := errors.New("callback failed")
	runtime.batchCallbacks[batchID] = batchCallbacks{
		then: func(context.Context, BatchState) error { return callbackErr },
	}
	env := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch_callback_failed_duplicate",
		JobID:         "job_callback_failed_duplicate",
		BatchID:       batchID,
		CallbackKind:  "batch_then",
	}
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobCallback, env); !errors.Is(err, callbackErr) {
		t.Fatalf("first callback error = %v, want %v", err, callbackErr)
	}
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobCallback, env); err != nil {
		t.Fatalf("duplicate callback delivery: %v", err)
	}
	failed := 0
	succeeded := 0
	for _, event := range recorder.events {
		switch event.Kind {
		case EventCallbackFailed:
			failed++
		case EventCallbackSucceeded:
			succeeded++
		}
	}
	if failed != 1 || succeeded != 0 {
		t.Fatalf("callback failed/succeeded events = %d/%d, want 1/0", failed, succeeded)
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
