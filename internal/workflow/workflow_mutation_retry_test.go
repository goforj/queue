package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/goforj/queue/busruntime"
)

type workflowMutationFaultStore struct {
	Store
	advanceChainErr       error
	failChainErr          error
	markBatchStartedErr   error
	markBatchSucceededErr error
	markBatchFailedErr    error
	cancelBatchErr        error
	getChainErr           error
	getBatchErr           error
	markCallbackErr       error
}

// AdvanceChain injects a chain progression persistence failure when configured.
func (s *workflowMutationFaultStore) AdvanceChain(ctx context.Context, chainID, completedNode string) (*ChainNode, bool, error) {
	if s.advanceChainErr != nil {
		return nil, false, s.advanceChainErr
	}
	return s.Store.AdvanceChain(ctx, chainID, completedNode)
}

// FailChain injects a terminal chain persistence failure when configured.
func (s *workflowMutationFaultStore) FailChain(ctx context.Context, chainID string, cause error) error {
	if s.failChainErr != nil {
		return s.failChainErr
	}
	return s.Store.FailChain(ctx, chainID, cause)
}

// MarkBatchJobStarted injects a batch-start persistence failure when configured.
func (s *workflowMutationFaultStore) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	if s.markBatchStartedErr != nil {
		return s.markBatchStartedErr
	}
	return s.Store.MarkBatchJobStarted(ctx, batchID, jobID)
}

// MarkBatchJobSucceeded injects a successful batch outcome persistence failure when configured.
func (s *workflowMutationFaultStore) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error) {
	if s.markBatchSucceededErr != nil {
		return BatchState{}, false, s.markBatchSucceededErr
	}
	return s.Store.MarkBatchJobSucceeded(ctx, batchID, jobID)
}

// MarkBatchJobFailed injects a failed batch outcome persistence failure when configured.
func (s *workflowMutationFaultStore) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (BatchState, bool, error) {
	if s.markBatchFailedErr != nil {
		return BatchState{}, false, s.markBatchFailedErr
	}
	return s.Store.MarkBatchJobFailed(ctx, batchID, jobID, cause)
}

// CancelBatch injects an initial batch cancellation persistence failure when configured.
func (s *workflowMutationFaultStore) CancelBatch(ctx context.Context, batchID string) error {
	if s.cancelBatchErr != nil {
		return s.cancelBatchErr
	}
	return s.Store.CancelBatch(ctx, batchID)
}

// GetChain injects a callback chain-state read failure when configured.
func (s *workflowMutationFaultStore) GetChain(ctx context.Context, chainID string) (ChainState, error) {
	if s.getChainErr != nil {
		return ChainState{}, s.getChainErr
	}
	return s.Store.GetChain(ctx, chainID)
}

// GetBatch injects a callback batch-state read failure when configured.
func (s *workflowMutationFaultStore) GetBatch(ctx context.Context, batchID string) (BatchState, error) {
	if s.getBatchErr != nil {
		return BatchState{}, s.getBatchErr
	}
	return s.Store.GetBatch(ctx, batchID)
}

// MarkCallbackInvoked injects a callback idempotency persistence failure when configured.
func (s *workflowMutationFaultStore) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	if s.markCallbackErr != nil {
		return false, s.markCallbackErr
	}
	return s.Store.MarkCallbackInvoked(ctx, key)
}

type workflowMutationEventRecorder struct {
	events []Event
}

// Observe records workflow facts synchronously for mutation-boundary assertions.
func (r *workflowMutationEventRecorder) Observe(_ context.Context, event Event) {
	r.events = append(r.events, event)
}

// newWorkflowMutationRuntime constructs the smallest runtime that can invoke internal workflow deliveries directly.
func newWorkflowMutationRuntime(t *testing.T, store Store) (*runtime, *syncTestRuntime, *workflowMutationEventRecorder) {
	t.Helper()
	queueRuntime := newSyncTestRuntime()
	recorder := &workflowMutationEventRecorder{}
	workflow, err := NewWithStore(queueRuntime, store, WithObserver(recorder))
	if err != nil {
		t.Fatalf("new workflow runtime: %v", err)
	}
	workflowRuntime, ok := workflow.(*runtime)
	if !ok {
		t.Fatalf("runtime type = %T", workflow)
	}
	return workflowRuntime, queueRuntime, recorder
}

// exhaustedWorkflowContext fixes the physical attempt at its application retry boundary.
func exhaustedWorkflowContext() context.Context {
	return busruntime.WithDeliveryAttempt(context.Background(), busruntime.DeliveryAttempt{Number: 2, MaxRetry: 2})
}

// assertUncommittedMutation verifies the store cause survives the same-attempt redelivery marker.
func assertUncommittedMutation(t *testing.T, err, storeErr error) {
	t.Helper()
	if !busruntime.IsUncommitted(err) || !errors.Is(err, storeErr) {
		t.Fatalf("mutation error = %v, want uncommitted store cause %v", err, storeErr)
	}
	if decision := busruntime.ClassifyAttempt(busruntime.DeliveryAttempt{Number: 2, MaxRetry: 2}, err); decision != busruntime.AttemptRedeliver {
		t.Fatalf("exhausted mutation decision = %v, want redeliver", decision)
	}
}

// assertNoCommittedEvents rejects terminal facts that require a successful workflow mutation.
func assertNoCommittedEvents(t *testing.T, events []Event, forbidden ...EventKind) {
	t.Helper()
	for _, event := range events {
		for _, kind := range forbidden {
			if event.Kind == kind {
				t.Fatalf("unexpected committed event %q in %+v", kind, events)
			}
		}
	}
}

// TestInitialDispatchRejectionRequiresTerminalStoreMutation verifies enqueue failure cannot fabricate chain or batch terminal facts.
func TestInitialDispatchRejectionRequiresTerminalStoreMutation(t *testing.T) {
	enqueueErr := errors.New("queue rejected initial workflow job")
	storeErr := errors.New("workflow terminal state unavailable")
	tests := []struct {
		name      string
		configure func(*workflowMutationFaultStore)
		dispatch  func(Engine) (string, error)
		forbidden []EventKind
	}{
		{
			name: "chain",
			configure: func(store *workflowMutationFaultStore) {
				store.failChainErr = storeErr
			},
			dispatch: func(workflow Engine) (string, error) {
				return workflow.Chain(NewJob("initial:chain", nil)).Dispatch(context.Background())
			},
			forbidden: []EventKind{EventChainFailed, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed},
		},
		{
			name: "batch",
			configure: func(store *workflowMutationFaultStore) {
				store.cancelBatchErr = storeErr
			},
			dispatch: func(workflow Engine) (string, error) {
				return workflow.Batch(NewJob("initial:batch", nil)).Dispatch(context.Background())
			},
			forbidden: []EventKind{EventBatchFailed, EventBatchCancelled, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseStore := NewMemoryStore()
			store := &workflowMutationFaultStore{Store: baseStore}
			test.configure(store)
			queueRuntime := newSyncTestRuntime()
			queueRuntime.dispatchErr = enqueueErr
			recorder := &workflowMutationEventRecorder{}
			workflow, err := NewWithStore(queueRuntime, store, WithObserver(recorder))
			if err != nil {
				t.Fatalf("new workflow: %v", err)
			}
			_, dispatchErr := test.dispatch(workflow)
			if !busruntime.IsUncommitted(dispatchErr) || !errors.Is(dispatchErr, enqueueErr) || !errors.Is(dispatchErr, storeErr) {
				t.Fatalf("dispatch error = %v, want uncommitted enqueue and store causes", dispatchErr)
			}
			assertNoCommittedEvents(t, recorder.events, test.forbidden...)
		})
	}
}

// TestInitialDispatchRejectionUsesObservedCallbackLifecycle verifies inline compatibility callbacks match queue-delivered observability.
func TestInitialDispatchRejectionUsesObservedCallbackLifecycle(t *testing.T) {
	enqueueErr := errors.New("queue rejected initial workflow job")
	tests := []struct {
		name      string
		dispatch  func(Engine, *int) (string, error)
		failed    EventKind
		cancelled bool
	}{
		{
			name: "chain",
			dispatch: func(workflow Engine, calls *int) (string, error) {
				return workflow.Chain(NewJob("initial:chain:callbacks", nil)).
					Catch(func(context.Context, ChainState, error) error { *calls++; return nil }).
					Finally(func(context.Context, ChainState) error { *calls++; return nil }).
					Dispatch(context.Background())
			},
			failed: EventChainFailed,
		},
		{
			name: "batch",
			dispatch: func(workflow Engine, calls *int) (string, error) {
				return workflow.Batch(NewJob("initial:batch:callbacks", nil)).
					Catch(func(context.Context, BatchState, error) error { *calls++; return nil }).
					Finally(func(context.Context, BatchState) error { *calls++; return nil }).
					Dispatch(context.Background())
			},
			failed:    EventBatchFailed,
			cancelled: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queueRuntime := newSyncTestRuntime()
			queueRuntime.dispatchErr = enqueueErr
			recorder := &workflowMutationEventRecorder{}
			workflow, err := NewWithStore(queueRuntime, NewMemoryStore(), WithObserver(recorder))
			if err != nil {
				t.Fatalf("new workflow: %v", err)
			}
			var calls int
			_, dispatchErr := test.dispatch(workflow, &calls)
			if !errors.Is(dispatchErr, enqueueErr) {
				t.Fatalf("dispatch error = %v, want %v", dispatchErr, enqueueErr)
			}
			if calls != 2 {
				t.Fatalf("callback calls = %d, want 2", calls)
			}
			var failed, cancelled, started, succeeded, callbackFailed int
			for _, event := range recorder.events {
				switch event.Kind {
				case test.failed:
					failed++
				case EventBatchCancelled:
					cancelled++
				case EventCallbackStarted:
					started++
				case EventCallbackSucceeded:
					succeeded++
				case EventCallbackFailed:
					callbackFailed++
				}
			}
			if failed != 1 || started != 2 || succeeded != 2 || callbackFailed != 0 {
				t.Fatalf("failed/started/succeeded/callback-failed = %d/%d/%d/%d, want 1/2/2/0", failed, started, succeeded, callbackFailed)
			}
			if test.cancelled != (cancelled == 1) {
				t.Fatalf("cancelled events = %d, expected=%t", cancelled, test.cancelled)
			}
		})
	}
}

// TestChainNextDispatchRejectionRemainsUncommitted verifies an advanced chain cannot settle until its next node is accepted.
func TestChainNextDispatchRejectionRemainsUncommitted(t *testing.T) {
	store := NewMemoryStore()
	const chainID = "chain_next_dispatch_rejected"
	first := StoredJob{Type: "chain:first"}
	second := StoredJob{Type: "chain:second"}
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID:    chainID,
		DispatchID: "dispatch_next_rejected",
		Nodes: []ChainNode{
			{NodeID: "node_first", Job: first},
			{NodeID: "node_second", Job: second},
		},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	queueRuntime := newSyncTestRuntime()
	workflow, err := NewWithStore(queueRuntime, store)
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	runtime := workflow.(*runtime)
	var firstCalls, secondCalls int
	runtime.Register(first.Type, func(context.Context, Context) error {
		firstCalls++
		return nil
	})
	runtime.Register(second.Type, func(context.Context, Context) error {
		secondCalls++
		return nil
	})
	payload, err := json.Marshal(envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch_next_rejected",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        "node_first",
		JobID:         "job_first",
		Job:           first,
	})
	if err != nil {
		t.Fatalf("marshal first node: %v", err)
	}
	rejection := errors.New("next node enqueue rejected")
	queueRuntime.dispatchErr = rejection
	firstErr := runtime.handleInternalChainNode(context.Background(), testInboundJob{payload: payload})
	if !busruntime.IsUncommitted(firstErr) || !errors.Is(firstErr, rejection) {
		t.Fatalf("first delivery error = %v, want uncommitted rejection", firstErr)
	}
	state, err := store.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get advanced chain: %v", err)
	}
	if state.NextIndex != 1 || state.Completed || state.Failed {
		t.Fatalf("state after rejected continuation = %+v", state)
	}

	queueRuntime.dispatchErr = nil
	if err := runtime.handleInternalChainNode(context.Background(), testInboundJob{payload: payload}); err != nil {
		t.Fatalf("redeliver first node: %v", err)
	}
	state, err = store.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get completed chain: %v", err)
	}
	if !state.Completed || state.Failed || state.NextIndex != 2 {
		t.Fatalf("completed chain state = %+v", state)
	}
	if firstCalls != 2 || secondCalls != 1 {
		t.Fatalf("handler calls = first:%d second:%d, want 2/1", firstCalls, secondCalls)
	}
}

// TestAllowFailuresBatchStopsOnUncommittedMutation verifies infrastructure failure cannot be mistaken for an allowed application failure.
func TestAllowFailuresBatchStopsOnUncommittedMutation(t *testing.T) {
	storeErr := errors.New("batch store unavailable")
	baseStore := NewMemoryStore()
	faultStore := &workflowMutationFaultStore{Store: baseStore, markBatchStartedErr: storeErr}
	runtime, _, _ := newWorkflowMutationRuntime(t, faultStore)
	var handlerCalls int
	runtime.Register("workflow:batch:first", func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	runtime.Register("workflow:batch:second", func(context.Context, Context) error {
		handlerCalls++
		return nil
	})

	batchID, err := runtime.Batch(
		NewJob("workflow:batch:first", nil),
		NewJob("workflow:batch:second", nil),
	).AllowFailures().Dispatch(context.Background())
	assertUncommittedMutation(t, err, storeErr)
	if handlerCalls != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls)
	}
	state, stateErr := baseStore.GetBatch(context.Background(), batchID)
	if stateErr != nil {
		t.Fatalf("get batch: %v", stateErr)
	}
	if state.Processed != 0 || state.Pending != 2 || state.Completed || state.Cancelled {
		t.Fatalf("batch advanced after uncommitted mutation: %+v", state)
	}
}

// TestChainDispatchDoesNotTerminalizeAcceptedMutationFailure verifies post-acceptance store errors remain redeliverable.
func TestChainDispatchDoesNotTerminalizeAcceptedMutationFailure(t *testing.T) {
	storeErr := errors.New("chain store unavailable")
	baseStore := NewMemoryStore()
	faultStore := &workflowMutationFaultStore{Store: baseStore, advanceChainErr: storeErr}
	runtime, _, recorder := newWorkflowMutationRuntime(t, faultStore)
	var handlerCalls int
	runtime.Register("workflow:chain:first", func(context.Context, Context) error {
		handlerCalls++
		return nil
	})

	chainID, err := runtime.Chain(NewJob("workflow:chain:first", nil)).Dispatch(context.Background())
	assertUncommittedMutation(t, err, storeErr)
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
	state, stateErr := baseStore.GetChain(context.Background(), chainID)
	if stateErr != nil {
		t.Fatalf("get chain: %v", stateErr)
	}
	if state.Failed || state.Completed || state.NextIndex != 0 {
		t.Fatalf("chain terminalized after uncommitted mutation: %+v", state)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted, EventChainFailed)
}

// TestChainMutationFailuresRedeliverExhaustedAttempt verifies store outages cannot terminally settle a chain.
func TestChainMutationFailuresRedeliverExhaustedAttempt(t *testing.T) {
	storeErr := errors.New("chain store unavailable")
	tests := []struct {
		name       string
		handlerErr error
		configure  func(*workflowMutationFaultStore)
	}{
		{
			name:       "terminal failure does not commit",
			handlerErr: errors.New("application failed"),
			configure: func(store *workflowMutationFaultStore) {
				store.failChainErr = storeErr
			},
		},
		{
			name: "successful node does not advance",
			configure: func(store *workflowMutationFaultStore) {
				store.advanceChainErr = storeErr
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				chainID = "chain_store_failure"
				nodeID  = "node_store_failure"
				jobID   = "job_store_failure"
				jobType = "workflow:chain:store-failure"
			)
			job := StoredJob{Type: jobType, Options: JobOptions{Retry: 2}}
			baseStore := NewMemoryStore()
			if err := baseStore.CreateChain(context.Background(), ChainRecord{
				ChainID: chainID,
				Nodes:   []ChainNode{{NodeID: nodeID, Job: job}},
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			faultStore := &workflowMutationFaultStore{Store: baseStore}
			test.configure(faultStore)
			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)

			handlerCalls := 0
			runtime.Register(jobType, func(context.Context, Context) error {
				handlerCalls++
				return test.handlerErr
			})
			catchCalls := 0
			finallyCalls := 0
			runtime.chainCallbacks[chainID] = chainCallbacks{
				catch: func(context.Context, ChainState, error) error {
					catchCalls++
					return nil
				},
				finally: func(context.Context, ChainState) error {
					finallyCalls++
					return nil
				},
			}

			err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch_store_failure",
				Kind:          "chain_node",
				ChainID:       chainID,
				NodeID:        nodeID,
				JobID:         jobID,
				Job:           job,
			})
			assertUncommittedMutation(t, err, storeErr)
			if handlerCalls != 1 {
				t.Fatalf("handler calls = %d, want 1", handlerCalls)
			}
			if catchCalls != 0 || finallyCalls != 0 {
				t.Fatalf("callbacks ran before state commit: catch=%d finally=%d", catchCalls, finallyCalls)
			}
			assertNoCommittedEvents(t, recorder.events,
				EventJobSucceeded,
				EventJobFailed,
				EventChainAdvanced,
				EventChainCompleted,
				EventChainFailed,
				EventCallbackSucceeded,
				EventCallbackFailed,
			)
			state, stateErr := baseStore.GetChain(context.Background(), chainID)
			if stateErr != nil {
				t.Fatalf("get chain: %v", stateErr)
			}
			if state.NextIndex != 0 || state.Completed || state.Failed {
				t.Fatalf("chain state committed despite store failure: %+v", state)
			}
		})
	}
}

// TestBatchMutationFailuresRedeliverExhaustedAttempt verifies every batch mutation gates execution and terminal facts.
func TestBatchMutationFailuresRedeliverExhaustedAttempt(t *testing.T) {
	storeErr := errors.New("batch store unavailable")
	tests := []struct {
		name             string
		handlerErr       error
		wantHandlerCalls int
		configure        func(*workflowMutationFaultStore)
	}{
		{
			name:             "started state does not commit",
			wantHandlerCalls: 0,
			configure: func(store *workflowMutationFaultStore) {
				store.markBatchStartedErr = storeErr
			},
		},
		{
			name:             "successful outcome does not commit",
			wantHandlerCalls: 1,
			configure: func(store *workflowMutationFaultStore) {
				store.markBatchSucceededErr = storeErr
			},
		},
		{
			name:             "failed outcome does not commit",
			handlerErr:       errors.New("application failed"),
			wantHandlerCalls: 1,
			configure: func(store *workflowMutationFaultStore) {
				store.markBatchFailedErr = storeErr
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				batchID = "batch_store_failure"
				jobID   = "job_store_failure"
				jobType = "workflow:batch:store-failure"
			)
			job := StoredJob{Type: jobType, Options: JobOptions{Retry: 2}}
			baseStore := NewMemoryStore()
			if err := baseStore.CreateBatch(context.Background(), BatchRecord{
				BatchID: batchID,
				Jobs:    []BatchJob{{JobID: jobID, Job: job}},
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			faultStore := &workflowMutationFaultStore{Store: baseStore}
			test.configure(faultStore)
			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)

			handlerCalls := 0
			runtime.Register(jobType, func(context.Context, Context) error {
				handlerCalls++
				return test.handlerErr
			})
			progressCalls := 0
			thenCalls := 0
			catchCalls := 0
			finallyCalls := 0
			runtime.batchCallbacks[batchID] = batchCallbacks{
				progress: func(context.Context, BatchState) error {
					progressCalls++
					return nil
				},
				then: func(context.Context, BatchState) error {
					thenCalls++
					return nil
				},
				catch: func(context.Context, BatchState, error) error {
					catchCalls++
					return nil
				},
				finally: func(context.Context, BatchState) error {
					finallyCalls++
					return nil
				},
			}

			err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobBatchJob, envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch_store_failure",
				Kind:          "batch_job",
				BatchID:       batchID,
				JobID:         jobID,
				Job:           job,
			})
			assertUncommittedMutation(t, err, storeErr)
			if handlerCalls != test.wantHandlerCalls {
				t.Fatalf("handler calls = %d, want %d", handlerCalls, test.wantHandlerCalls)
			}
			if progressCalls != 0 || thenCalls != 0 || catchCalls != 0 || finallyCalls != 0 {
				t.Fatalf("callbacks ran before state commit: progress=%d then=%d catch=%d finally=%d", progressCalls, thenCalls, catchCalls, finallyCalls)
			}
			assertNoCommittedEvents(t, recorder.events,
				EventJobSucceeded,
				EventJobFailed,
				EventBatchProgressed,
				EventBatchCompleted,
				EventBatchFailed,
				EventBatchCancelled,
				EventCallbackSucceeded,
				EventCallbackFailed,
			)
			state, stateErr := baseStore.GetBatch(context.Background(), batchID)
			if stateErr != nil {
				t.Fatalf("get batch: %v", stateErr)
			}
			if state.Pending != 1 || state.Processed != 0 || state.Failed != 0 || state.Completed || state.Cancelled {
				t.Fatalf("batch terminal state committed despite store failure: %+v", state)
			}
		})
	}
}

// TestCallbackStoreFailuresRedeliverWithoutTerminalFacts verifies state reads and idempotency writes remain retryable at exhaustion.
func TestCallbackStoreFailuresRedeliverWithoutTerminalFacts(t *testing.T) {
	storeErr := errors.New("callback store unavailable")
	tests := []struct {
		name        string
		callbackEnv envelope
		seed        func(context.Context, Store) error
		configure   func(*workflowMutationFaultStore)
		clear       func(*workflowMutationFaultStore)
		install     func(*runtime, *int)
	}{
		{
			name: "chain state read",
			callbackEnv: envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch_callback_store_failure",
				JobID:         "job_callback_store_failure",
				ChainID:       "chain_callback_store_failure",
				CallbackKind:  "chain_finally",
			},
			seed: func(ctx context.Context, store Store) error {
				if err := store.CreateChain(ctx, ChainRecord{
					ChainID: "chain_callback_store_failure",
					Nodes:   []ChainNode{{NodeID: "chain_callback_node", Job: StoredJob{Type: "callback:source"}}},
				}); err != nil {
					return err
				}
				_, _, err := store.AdvanceChain(ctx, "chain_callback_store_failure", "chain_callback_node")
				return err
			},
			configure: func(store *workflowMutationFaultStore) {
				store.getChainErr = storeErr
			},
			clear: func(store *workflowMutationFaultStore) {
				store.getChainErr = nil
			},
			install: func(runtime *runtime, calls *int) {
				runtime.chainCallbacks["chain_callback_store_failure"] = chainCallbacks{
					finally: func(context.Context, ChainState) error {
						*calls = *calls + 1
						return nil
					},
				}
			},
		},
		{
			name: "batch state read",
			callbackEnv: envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch_callback_store_failure",
				JobID:         "job_callback_store_failure",
				BatchID:       "batch_callback_store_failure",
				CallbackKind:  "batch_then",
			},
			seed: func(ctx context.Context, store Store) error {
				if err := store.CreateBatch(ctx, BatchRecord{
					BatchID: "batch_callback_store_failure",
					Jobs:    []BatchJob{{JobID: "batch_callback_job", Job: StoredJob{Type: "callback:source"}}},
				}); err != nil {
					return err
				}
				_, _, err := store.MarkBatchJobSucceeded(ctx, "batch_callback_store_failure", "batch_callback_job")
				return err
			},
			configure: func(store *workflowMutationFaultStore) {
				store.getBatchErr = storeErr
			},
			clear: func(store *workflowMutationFaultStore) {
				store.getBatchErr = nil
			},
			install: func(runtime *runtime, calls *int) {
				runtime.batchCallbacks["batch_callback_store_failure"] = batchCallbacks{
					then: func(context.Context, BatchState) error {
						*calls = *calls + 1
						return nil
					},
				}
			},
		},
		{
			name: "callback idempotency write",
			callbackEnv: envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch_callback_store_failure",
				JobID:         "job_callback_store_failure",
				BatchID:       "batch_callback_store_failure",
				CallbackKind:  "batch_then",
			},
			seed: func(ctx context.Context, store Store) error {
				if err := store.CreateBatch(ctx, BatchRecord{
					BatchID: "batch_callback_store_failure",
					Jobs:    []BatchJob{{JobID: "batch_callback_job", Job: StoredJob{Type: "callback:source"}}},
				}); err != nil {
					return err
				}
				_, _, err := store.MarkBatchJobSucceeded(ctx, "batch_callback_store_failure", "batch_callback_job")
				return err
			},
			configure: func(store *workflowMutationFaultStore) {
				store.markCallbackErr = storeErr
			},
			clear: func(store *workflowMutationFaultStore) {
				store.markCallbackErr = nil
			},
			install: func(runtime *runtime, calls *int) {
				runtime.batchCallbacks["batch_callback_store_failure"] = batchCallbacks{
					then: func(context.Context, BatchState) error {
						*calls = *calls + 1
						return nil
					},
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseStore := NewMemoryStore()
			if err := test.seed(context.Background(), baseStore); err != nil {
				t.Fatalf("seed workflow state: %v", err)
			}
			faultStore := &workflowMutationFaultStore{Store: baseStore}
			test.configure(faultStore)
			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)
			callbackCalls := 0
			test.install(runtime, &callbackCalls)

			err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobCallback, test.callbackEnv)
			assertUncommittedMutation(t, err, storeErr)
			if callbackCalls != 0 {
				t.Fatalf("callback calls before store recovery = %d, want 0", callbackCalls)
			}
			assertNoCommittedEvents(t, recorder.events, EventCallbackSucceeded, EventCallbackFailed)

			test.clear(faultStore)
			if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobCallback, test.callbackEnv); err != nil {
				t.Fatalf("callback retry after store recovery: %v", err)
			}
			if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobCallback, test.callbackEnv); err != nil {
				t.Fatalf("duplicate callback delivery: %v", err)
			}
			if callbackCalls != 1 {
				t.Fatalf("callback calls after retry and duplicate = %d, want 1", callbackCalls)
			}
			succeeded := 0
			for _, event := range recorder.events {
				if event.Kind == EventCallbackSucceeded {
					succeeded++
				}
			}
			if succeeded != 1 {
				t.Fatalf("callback success events after retry and duplicate = %d, want 1", succeeded)
			}
		})
	}
}
