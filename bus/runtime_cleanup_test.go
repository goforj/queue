package bus

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingBatchCompletionStore struct {
	Store
	blockedJob string
	committed  chan struct{}
	release    chan struct{}
	once       sync.Once
}

// MarkBatchJobSucceeded pauses one already-committed outcome so another job can prepare terminal callbacks first.
func (s *blockingBatchCompletionStore) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error) {
	state, done, err := s.Store.MarkBatchJobSucceeded(ctx, batchID, jobID)
	if err == nil && jobID == s.blockedJob {
		s.once.Do(func() { close(s.committed) })
		<-s.release
	}
	return state, done, err
}

func TestRuntimeCleansChainCallbacksAfterFinally(t *testing.T) {
	q := newSyncTestRuntime()
	bi, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	r := bi.(*runtime)
	if err := r.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	r.Register("monitor:poll", func(context.Context, Context) error { return nil })
	if _, err := r.Chain(NewJob("monitor:poll", nil)).
		Finally(func(context.Context, ChainState) error { return nil }).
		Dispatch(context.Background()); err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}

	r.mu.RLock()
	n := len(r.chainCallbacks)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expected chain callbacks map cleaned, got len=%d", n)
	}
}

func TestRuntimeCleansBatchCallbacksAfterFinally(t *testing.T) {
	q := newSyncTestRuntime()
	bi, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	r := bi.(*runtime)
	if err := r.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	r.Register("monitor:poll", func(context.Context, Context) error { return nil })
	if _, err := r.Batch(NewJob("monitor:poll", nil)).
		Finally(func(context.Context, BatchState) error { return nil }).
		Dispatch(context.Background()); err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}

	r.mu.RLock()
	n := len(r.batchCallbacks)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expected batch callbacks map cleaned, got len=%d", n)
	}
}

// TestChainFinallyPreservesPendingCatch verifies independently delivered terminal callbacks do not delete one another.
func TestChainFinallyPreservesPendingCatch(t *testing.T) {
	const chainID = "chain_out_of_order_callbacks"
	var catchCount int
	var finallyCount int
	runtime := &runtime{
		store: NewMemoryStore(),
		chainCallbacks: map[string]chainCallbacks{
			chainID: {
				catch: func(context.Context, ChainState, error) error {
					catchCount++
					return nil
				},
				finally: func(context.Context, ChainState) error {
					finallyCount++
					return nil
				},
			},
		},
	}
	state := ChainState{ChainID: chainID, Failed: true}

	if err := runtime.invokeChainFinally(context.Background(), state); err != nil {
		t.Fatalf("invoke finally: %v", err)
	}
	if err := runtime.invokeChainCatch(context.Background(), state, context.Canceled); err != nil {
		t.Fatalf("invoke catch: %v", err)
	}
	if catchCount != 1 || finallyCount != 1 {
		t.Fatalf("catch/finally count = %d/%d, want 1/1", catchCount, finallyCount)
	}
	if len(runtime.chainCallbacks) != 0 {
		t.Fatalf("expected chain callbacks cleaned, got len=%d", len(runtime.chainCallbacks))
	}
}

// TestBatchFinallyPreservesPendingTerminalCallbacks verifies terminal cleanup is independent of delivery order.
func TestBatchFinallyPreservesPendingTerminalCallbacks(t *testing.T) {
	const batchID = "batch_out_of_order_callbacks"
	var catchCount int
	var thenCount int
	var finallyCount int
	runtime := &runtime{
		store: NewMemoryStore(),
		batchCallbacks: map[string]batchCallbacks{
			batchID: {
				progress: func(context.Context, BatchState) error {
					t.Fatal("terminal preparation retained progress callback")
					return nil
				},
				then: func(context.Context, BatchState) error {
					thenCount++
					return nil
				},
				catch: func(context.Context, BatchState, error) error {
					catchCount++
					return nil
				},
				finally: func(context.Context, BatchState) error {
					finallyCount++
					return nil
				},
			},
		},
	}
	state := BatchState{BatchID: batchID, Failed: 1, AllowFailed: true, Completed: true}
	runtime.prepareBatchTerminalCallbacks(batchID, true, true)

	if err := runtime.invokeBatchFinally(context.Background(), state); err != nil {
		t.Fatalf("invoke finally: %v", err)
	}
	if err := runtime.invokeBatchThen(context.Background(), state); err != nil {
		t.Fatalf("invoke then: %v", err)
	}
	if err := runtime.invokeBatchCatch(context.Background(), state, context.Canceled); err != nil {
		t.Fatalf("invoke catch: %v", err)
	}
	if catchCount != 1 || thenCount != 1 || finallyCount != 1 {
		t.Fatalf("catch/then/finally count = %d/%d/%d, want 1/1/1", catchCount, thenCount, finallyCount)
	}
	if len(runtime.batchCallbacks) != 0 {
		t.Fatalf("expected batch callbacks cleaned, got len=%d", len(runtime.batchCallbacks))
	}
}

// TestBatchCallbackEnvelopesCompleteInReverseOrder verifies serialized deliveries preserve sibling callbacks, lifecycle facts, and idempotency.
func TestBatchCallbackEnvelopesCompleteInReverseOrder(t *testing.T) {
	const batchID = "batch_reverse_callback_envelopes"
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID:     batchID,
		DispatchID:  "dispatch_reverse_callback_envelopes",
		AllowFailed: true,
		Jobs: []BatchJob{
			{JobID: "job_failed", Job: wireJob{Type: "batch:failure"}},
			{JobID: "job_succeeded", Job: wireJob{Type: "batch:success"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if _, _, err := store.MarkBatchJobFailed(context.Background(), batchID, "job_failed", errors.New("allowed failure")); err != nil {
		t.Fatalf("mark failed job: %v", err)
	}
	state, done, err := store.MarkBatchJobSucceeded(context.Background(), batchID, "job_succeeded")
	if err != nil {
		t.Fatalf("mark successful job: %v", err)
	}
	if !done || !state.Completed || state.Failed != 1 || state.Cancelled {
		t.Fatalf("terminal batch state = %+v, done=%t", state, done)
	}

	var callbackCalls []string
	var events []Event
	runtime := &runtime{
		store: store,
		now:   time.Now,
		observer: ObserverFunc(func(_ context.Context, event Event) {
			events = append(events, event)
		}),
		chainCallbacks: make(map[string]chainCallbacks),
		batchCallbacks: map[string]batchCallbacks{
			batchID: {
				then: func(context.Context, BatchState) error {
					callbackCalls = append(callbackCalls, "then")
					return nil
				},
				catch: func(context.Context, BatchState, error) error {
					callbackCalls = append(callbackCalls, "catch")
					return nil
				},
				finally: func(context.Context, BatchState) error {
					callbackCalls = append(callbackCalls, "finally")
					return nil
				},
			},
		},
	}
	runtime.prepareBatchTerminalCallbacks(batchID, true, true)

	deliver := func(kind string) {
		env := envelope{
			SchemaVersion: schemaVersion,
			DispatchID:    state.DispatchID,
			JobID:         "callback_" + kind,
			BatchID:       batchID,
			Job:           wireJob{Options: JobOptions{Queue: "bulk"}},
			CallbackKind:  kind,
			Error:         "allowed failure",
		}
		payload, marshalErr := json.Marshal(env)
		if marshalErr != nil {
			t.Fatalf("marshal %s callback: %v", kind, marshalErr)
		}
		if callbackErr := runtime.handleInternalCallback(context.Background(), testInboundJob{payload: payload}); callbackErr != nil {
			t.Fatalf("deliver %s callback: %v", kind, callbackErr)
		}
	}

	deliver("batch_finally")
	deliver("batch_then")
	deliver("batch_catch")
	if got := callbackCalls; len(got) != 3 || got[0] != "finally" || got[1] != "then" || got[2] != "catch" {
		t.Fatalf("callback order = %v, want [finally then catch]", got)
	}
	if len(runtime.batchCallbacks) != 0 {
		t.Fatalf("callback state retained after all siblings completed: %+v", runtime.batchCallbacks)
	}

	var started, succeeded int
	for _, event := range events {
		switch event.Kind {
		case EventCallbackStarted:
			started++
		case EventCallbackSucceeded:
			succeeded++
		}
	}
	if started != 3 || succeeded != 3 {
		t.Fatalf("callback lifecycle facts = started:%d succeeded:%d, want 3/3", started, succeeded)
	}

	deliver("batch_finally")
	if len(events) != 6 || len(callbackCalls) != 3 {
		t.Fatalf("duplicate callback emitted facts or ran application code: events=%d calls=%v", len(events), callbackCalls)
	}
}

// TestConcurrentBatchProgressUsesPerDeliverySnapshot verifies terminal cleanup cannot erase an earlier committed job's Progress hook.
func TestConcurrentBatchProgressUsesPerDeliverySnapshot(t *testing.T) {
	const batchID = "batch_concurrent_progress"
	baseStore := NewMemoryStore()
	if err := baseStore.CreateBatch(context.Background(), BatchRecord{
		BatchID: batchID,
		Jobs: []BatchJob{
			{JobID: "job_paused", Job: wireJob{Type: "batch:item"}},
			{JobID: "job_final", Job: wireJob{Type: "batch:item"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	store := &blockingBatchCompletionStore{
		Store:      baseStore,
		blockedJob: "job_paused",
		committed:  make(chan struct{}),
		release:    make(chan struct{}),
	}
	var progressCalls atomic.Int32
	runtime := &runtime{
		store: store,
		now:   time.Now,
		handlers: map[string]Handler{
			"batch:item": func(context.Context, Context) error { return nil },
		},
		chainCallbacks: make(map[string]chainCallbacks),
		batchCallbacks: map[string]batchCallbacks{
			batchID: {
				progress: func(context.Context, BatchState) error {
					progressCalls.Add(1)
					return nil
				},
			},
		},
	}

	delivery := func(jobID string) testInboundJob {
		payload, err := json.Marshal(envelope{
			SchemaVersion: schemaVersion,
			DispatchID:    "dispatch_concurrent_progress",
			Kind:          "batch_job",
			BatchID:       batchID,
			JobID:         jobID,
			Job:           wireJob{Type: "batch:item"},
		})
		if err != nil {
			t.Fatalf("marshal %s delivery: %v", jobID, err)
		}
		return testInboundJob{payload: payload}
	}
	pausedDelivery := delivery("job_paused")
	finalDelivery := delivery("job_final")

	pausedResult := make(chan error, 1)
	go func() {
		pausedResult <- runtime.handleInternalBatchJob(context.Background(), pausedDelivery)
	}()
	<-store.committed
	if err := runtime.handleInternalBatchJob(context.Background(), finalDelivery); err != nil {
		t.Fatalf("final job: %v", err)
	}
	close(store.release)
	if err := <-pausedResult; err != nil {
		t.Fatalf("paused job: %v", err)
	}
	if got := progressCalls.Load(); got != 2 {
		t.Fatalf("progress calls = %d, want one for each processed job", got)
	}
}

// TestAllowFailuresBatchCompletesIndependentOfFailureOrder verifies aggregate outcome does not depend on the final physical job.
func TestAllowFailuresBatchCompletesIndependentOfFailureOrder(t *testing.T) {
	for _, failureFirst := range []bool{true, false} {
		name := "failure_last"
		if failureFirst {
			name = "failure_first"
		}
		t.Run(name, func(t *testing.T) {
			queueRuntime := newSyncTestRuntime()
			var events []Event
			busRuntime, err := New(queueRuntime, WithObserver(ObserverFunc(func(_ context.Context, event Event) {
				events = append(events, event)
			})))
			if err != nil {
				t.Fatalf("new bus: %v", err)
			}
			runtime := busRuntime.(*runtime)
			if err := runtime.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start workers: %v", err)
			}
			t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })

			failureErr := errors.New("allowed job failure")
			var handled int
			runtime.Register("batch:success", func(context.Context, Context) error {
				handled++
				return nil
			})
			runtime.Register("batch:failure", func(context.Context, Context) error {
				handled++
				return failureErr
			})
			jobs := []Job{NewJob("batch:success", nil), NewJob("batch:failure", nil)}
			if failureFirst {
				jobs[0], jobs[1] = jobs[1], jobs[0]
			}

			var catchCount int
			var thenCount int
			var finallyCount int
			batchID, dispatchErr := runtime.Batch(jobs...).
				AllowFailures().
				Catch(func(_ context.Context, _ BatchState, callbackErr error) error {
					if callbackErr == nil || callbackErr.Error() != failureErr.Error() {
						t.Fatalf("catch error = %v, want %v", callbackErr, failureErr)
					}
					catchCount++
					return nil
				}).
				Then(func(context.Context, BatchState) error {
					thenCount++
					return nil
				}).
				Finally(func(context.Context, BatchState) error {
					finallyCount++
					return nil
				}).
				Dispatch(context.Background())
			if !errors.Is(dispatchErr, failureErr) {
				t.Fatalf("dispatch error = %v, want %v", dispatchErr, failureErr)
			}
			state, err := runtime.FindBatch(context.Background(), batchID)
			if err != nil {
				t.Fatalf("find batch: %v", err)
			}
			if handled != 2 || state.Processed != 2 || state.Pending != 0 || state.Failed != 1 || !state.Completed || state.Cancelled {
				t.Fatalf("handled/state = %d/%+v, want two processed and completed with one allowed failure", handled, state)
			}
			if catchCount != 1 || thenCount != 1 || finallyCount != 1 {
				t.Fatalf("catch/then/finally count = %d/%d/%d, want 1/1/1", catchCount, thenCount, finallyCount)
			}
			var progressed, completed, failed int
			for _, event := range events {
				switch event.Kind {
				case EventBatchProgressed:
					progressed++
				case EventBatchCompleted:
					completed++
				case EventBatchFailed:
					failed++
				}
			}
			if progressed != 2 || completed != 1 || failed != 0 {
				t.Fatalf("batch progressed/completed/failed events = %d/%d/%d, want 2/1/0", progressed, completed, failed)
			}
			runtime.mu.RLock()
			callbackCount := len(runtime.batchCallbacks)
			runtime.mu.RUnlock()
			if callbackCount != 0 {
				t.Fatalf("expected batch callbacks cleaned, got len=%d", callbackCount)
			}
		})
	}
}

// TestCallbackStateValidationPreservesLegitimateInvocation verifies premature jobs cannot consume callback markers.
func TestCallbackStateValidationPreservesLegitimateInvocation(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*runtime, *int)
		invalid   func(*runtime) error
		valid     func(*runtime) error
	}{
		{
			name: "chain catch",
			configure: func(runtime *runtime, calls *int) {
				runtime.chainCallbacks["chain_state"] = chainCallbacks{catch: func(context.Context, ChainState, error) error { *calls++; return nil }}
			},
			invalid: func(runtime *runtime) error {
				return runtime.invokeChainCatch(context.Background(), ChainState{ChainID: "chain_state"}, context.Canceled)
			},
			valid: func(runtime *runtime) error {
				return runtime.invokeChainCatch(context.Background(), ChainState{ChainID: "chain_state", Failed: true}, context.Canceled)
			},
		},
		{
			name: "chain finally",
			configure: func(runtime *runtime, calls *int) {
				runtime.chainCallbacks["chain_state"] = chainCallbacks{finally: func(context.Context, ChainState) error { *calls++; return nil }}
			},
			invalid: func(runtime *runtime) error {
				return runtime.invokeChainFinally(context.Background(), ChainState{ChainID: "chain_state"})
			},
			valid: func(runtime *runtime) error {
				return runtime.invokeChainFinally(context.Background(), ChainState{ChainID: "chain_state", Completed: true})
			},
		},
		{
			name: "batch catch",
			configure: func(runtime *runtime, calls *int) {
				runtime.batchCallbacks["batch_state"] = batchCallbacks{catch: func(context.Context, BatchState, error) error { *calls++; return nil }}
			},
			invalid: func(runtime *runtime) error {
				return runtime.invokeBatchCatch(context.Background(), BatchState{BatchID: "batch_state"}, context.Canceled)
			},
			valid: func(runtime *runtime) error {
				return runtime.invokeBatchCatch(context.Background(), BatchState{BatchID: "batch_state", Failed: 1}, context.Canceled)
			},
		},
		{
			name: "batch then incomplete",
			configure: func(runtime *runtime, calls *int) {
				runtime.batchCallbacks["batch_state"] = batchCallbacks{then: func(context.Context, BatchState) error { *calls++; return nil }}
			},
			invalid: func(runtime *runtime) error {
				return runtime.invokeBatchThen(context.Background(), BatchState{BatchID: "batch_state"})
			},
			valid: func(runtime *runtime) error {
				return runtime.invokeBatchThen(context.Background(), BatchState{BatchID: "batch_state", Completed: true})
			},
		},
		{
			name: "batch then cancelled",
			configure: func(runtime *runtime, calls *int) {
				runtime.batchCallbacks["batch_state"] = batchCallbacks{then: func(context.Context, BatchState) error { *calls++; return nil }}
			},
			invalid: func(runtime *runtime) error {
				return runtime.invokeBatchThen(context.Background(), BatchState{BatchID: "batch_state", Completed: true, Cancelled: true})
			},
			valid: func(runtime *runtime) error {
				return runtime.invokeBatchThen(context.Background(), BatchState{BatchID: "batch_state", Completed: true})
			},
		},
		{
			name: "batch finally",
			configure: func(runtime *runtime, calls *int) {
				runtime.batchCallbacks["batch_state"] = batchCallbacks{finally: func(context.Context, BatchState) error { *calls++; return nil }}
			},
			invalid: func(runtime *runtime) error {
				return runtime.invokeBatchFinally(context.Background(), BatchState{BatchID: "batch_state"})
			},
			valid: func(runtime *runtime) error {
				return runtime.invokeBatchFinally(context.Background(), BatchState{BatchID: "batch_state", Completed: true})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &runtime{
				store:          NewMemoryStore(),
				chainCallbacks: make(map[string]chainCallbacks),
				batchCallbacks: make(map[string]batchCallbacks),
			}
			var calls int
			test.configure(runtime, &calls)
			if err := test.invalid(runtime); !errors.Is(err, errCallbackNotReady) {
				t.Fatalf("invalid state error = %v, want errCallbackNotReady", err)
			}
			if calls != 0 {
				t.Fatalf("premature callback calls = %d, want 0", calls)
			}
			if err := test.valid(runtime); err != nil {
				t.Fatalf("valid callback: %v", err)
			}
			if calls != 1 {
				t.Fatalf("legitimate callback calls = %d, want 1", calls)
			}
		})
	}
}

// TestBatchProgressPanicDoesNotBlockTerminalCallbacks verifies ephemeral progress cannot unwind committed batch completion.
func TestBatchProgressPanicDoesNotBlockTerminalCallbacks(t *testing.T) {
	queueRuntime := newSyncTestRuntime()
	busRuntime, err := New(queueRuntime)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	runtime := busRuntime.(*runtime)
	runtime.Register("batch:progress-panic", func(context.Context, Context) error { return nil })
	var thenCalls int
	var finallyCalls int
	_, err = runtime.Batch(NewJob("batch:progress-panic", nil)).
		Progress(func(context.Context, BatchState) error { panic("progress panic") }).
		Then(func(context.Context, BatchState) error { thenCalls++; return nil }).
		Finally(func(context.Context, BatchState) error { finallyCalls++; return nil }).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	if thenCalls != 1 || finallyCalls != 1 {
		t.Fatalf("then/finally calls = %d/%d, want 1/1", thenCalls, finallyCalls)
	}
}
