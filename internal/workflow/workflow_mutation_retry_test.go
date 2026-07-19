package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/queue/busruntime"
)

type workflowMutationFaultStore struct {
	Store
	advanceChainErr         error
	failChainErr            error
	markBatchStartedErr     error
	markBatchSucceededErr   error
	markBatchFailedErr      error
	cancelBatchErr          error
	getChainErr             error
	getChainErrOnCall       int
	getChainCalls           int
	getChainState           *ChainState
	getBatchErr             error
	getBatchState           *BatchState
	markCallbackErr         error
	advanceDoneWithoutState bool
	failChainWithoutState   bool
}

type nonterminalWorkflowOutcomeStore struct {
	Store
}

// compatibilityOutcomeStore exposes only the public outcome capability so
// runtime tests cannot accidentally inherit a built-in private claim method.
type compatibilityOutcomeStore struct {
	Store
}

// FailChainNode claims failure without committing terminal state to exercise runtime confirmation.
func (s nonterminalWorkflowOutcomeStore) FailChainNode(ctx context.Context, chainID, _ string, _ error) (ChainState, bool, error) {
	state, err := s.Store.GetChain(ctx, chainID)
	return state, true, err
}

// SettleBatchJob is unused by the chain-focused fault but completes the atomic capability contract.
func (s nonterminalWorkflowOutcomeStore) SettleBatchJob(context.Context, string, string, BatchJobOutcome, error) (BatchState, bool, error) {
	return BatchState{}, false, errors.New("unexpected batch settlement")
}

// FailChainNode delegates the public outcome capability without exposing any
// private transition claim metadata.
func (s compatibilityOutcomeStore) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error) {
	store, ok := s.Store.(outcomeStore)
	if !ok {
		return ChainState{}, false, errors.New("wrapped store does not support outcome arbitration")
	}
	return store.FailChainNode(ctx, chainID, nodeID, cause)
}

// SettleBatchJob delegates the public outcome capability without exposing any
// private transition claim metadata.
func (s compatibilityOutcomeStore) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error) (BatchState, bool, error) {
	store, ok := s.Store.(outcomeStore)
	if !ok {
		return BatchState{}, false, errors.New("wrapped store does not support outcome arbitration")
	}
	return store.SettleBatchJob(ctx, batchID, jobID, outcome, cause)
}

// AdvanceChain injects a chain progression persistence failure when configured.
func (s *workflowMutationFaultStore) AdvanceChain(ctx context.Context, chainID, completedNode string) (*ChainNode, bool, error) {
	if s.advanceChainErr != nil {
		return nil, false, s.advanceChainErr
	}
	if s.advanceDoneWithoutState {
		return nil, true, nil
	}
	return s.Store.AdvanceChain(ctx, chainID, completedNode)
}

// FailChain injects a terminal chain persistence failure when configured.
func (s *workflowMutationFaultStore) FailChain(ctx context.Context, chainID string, cause error) error {
	if s.failChainErr != nil {
		return s.failChainErr
	}
	if s.failChainWithoutState {
		return nil
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
	s.getChainCalls++
	if s.getChainErr != nil {
		return ChainState{}, s.getChainErr
	}
	if s.getChainErrOnCall > 0 && s.getChainCalls == s.getChainErrOnCall {
		return ChainState{}, errors.New("injected chain read failure")
	}
	if s.getChainState != nil {
		return *s.getChainState, nil
	}
	return s.Store.GetChain(ctx, chainID)
}

// GetBatch injects a callback batch-state read failure when configured.
func (s *workflowMutationFaultStore) GetBatch(ctx context.Context, batchID string) (BatchState, error) {
	if s.getBatchErr != nil {
		return BatchState{}, s.getBatchErr
	}
	if s.getBatchState != nil {
		return *s.getBatchState, nil
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

// workflowGenerationContext attaches one opaque settlement generation to a
// direct workflow-delivery test context.
func workflowGenerationContext(ctx context.Context, generationID string) context.Context {
	return busruntime.WithDeliveryProvenance(ctx, busruntime.DeliveryProvenance{GenerationID: generationID})
}

// workflowRecoveryContext identifies both the current claim and the earlier
// unsettled generation whose receipt may be reconstructed.
func workflowRecoveryContext(ctx context.Context, generationID, recoveredGenerationID string) context.Context {
	return busruntime.WithDeliveryProvenance(ctx, busruntime.DeliveryProvenance{
		GenerationID:          generationID,
		RecoveredGenerationID: recoveredGenerationID,
		Recovered:             true,
	})
}

// workflowTransitionClaim creates the durable receipt identity expected from
// one direct workflow-delivery fixture.
func workflowTransitionClaim(env envelope, attempt int, generationID string) transitionClaim {
	return transitionClaim{
		deliveryID:     generationID,
		attempt:        attempt,
		dispatchID:     env.DispatchID,
		jobID:          env.JobID,
		jobFingerprint: storedJobReceiptFingerprint(env.Job),
	}
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

// TestSuccessfulDuplicateCannotCompleteFailedChain proves a competing failure
// remains authoritative when the same physical node also returns success.
func TestSuccessfulDuplicateCannotCompleteFailedChain(t *testing.T) {
	const (
		chainID = "chain-concurrent-terminal-outcome"
		nodeID  = "node-concurrent-terminal-outcome"
		jobType = "workflow:chain:concurrent-terminal-outcome"
	)
	store := NewMemoryStore()
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes: []ChainNode{{
			NodeID: nodeID,
			Job:    StoredJob{Type: jobType},
		}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if err := store.FailChain(context.Background(), chainID, errors.New("competing delivery failed")); err != nil {
		t.Fatalf("fail chain: %v", err)
	}
	runtime, _, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls, callbackCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	runtime.chainCallbacks[chainID] = chainCallbacks{
		finally: func(context.Context, ChainState) error {
			callbackCalls++
			return nil
		},
	}
	payload, err := json.Marshal(envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-concurrent-terminal-outcome",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-concurrent-terminal-outcome",
		Job:           StoredJob{Type: jobType},
	})
	if err != nil {
		t.Fatalf("marshal duplicate node: %v", err)
	}
	if err := runtime.handleInternalChainNode(context.Background(), testInboundJob{payload: payload}); err != nil {
		t.Fatalf("handle successful duplicate: %v", err)
	}
	if handlerCalls != 1 || callbackCalls != 0 {
		t.Fatalf("handler/callback calls = %d/%d, want 1/0", handlerCalls, callbackCalls)
	}
	state, err := store.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	if !state.Failed || state.Completed {
		t.Fatalf("chain state = %+v, want failed only", state)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)
}

// TestCompletedChainPublishesOnlyFinalNodeReplay preserves post-commit
// recovery without letting an earlier node impersonate terminal completion.
func TestCompletedChainPublishesOnlyFinalNodeReplay(t *testing.T) {
	const (
		chainID      = "chain-completed-node-replay"
		firstNodeID  = "node-completed-first"
		finalNodeID  = "node-completed-final"
		firstJobType = "workflow:chain:completed-first"
		finalJobType = "workflow:chain:completed-final"
	)
	store := NewMemoryStore()
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes: []ChainNode{
			{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType}},
			{NodeID: finalNodeID, Job: StoredJob{Type: finalJobType}},
		},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if _, _, err := store.AdvanceChain(context.Background(), chainID, firstNodeID); err != nil {
		t.Fatalf("advance first node: %v", err)
	}
	if _, done, err := store.AdvanceChain(context.Background(), chainID, finalNodeID); err != nil || !done {
		t.Fatalf("complete final node = done:%t err:%v", done, err)
	}

	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var firstCalls, finalCalls, finallyCalls int
	runtime.Register(firstJobType, func(context.Context, Context) error {
		firstCalls++
		return nil
	})
	runtime.Register(finalJobType, func(context.Context, Context) error {
		finalCalls++
		return nil
	})
	runtime.chainCallbacks[chainID] = chainCallbacks{
		finally: func(context.Context, ChainState) error {
			finallyCalls++
			return nil
		},
	}
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-completed-node-replay",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        firstNodeID,
		JobID:         "job-completed-first-replay",
		Job:           StoredJob{Type: firstJobType},
	}); err != nil {
		t.Fatalf("replay stale first node: %v", err)
	}
	if firstCalls != 1 || finalCalls != 0 || finallyCalls != 0 {
		t.Fatalf("calls after stale replay = first:%d final:%d finally:%d, want 1/0/0", firstCalls, finalCalls, finallyCalls)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed)

	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-completed-node-replay",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        finalNodeID,
		JobID:         "job-completed-final-replay",
		Job:           StoredJob{Type: finalJobType},
	}); err != nil {
		t.Fatalf("replay final node: %v", err)
	}
	if firstCalls != 1 || finalCalls != 1 || finallyCalls != 1 {
		t.Fatalf("calls after final replay = first:%d final:%d finally:%d, want 1/1/1", firstCalls, finalCalls, finallyCalls)
	}
	var succeeded, completed, callbackSucceeded int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventChainCompleted:
			completed++
		case EventCallbackSucceeded:
			callbackSucceeded++
		}
	}
	if succeeded != 0 || completed != 0 || callbackSucceeded != 1 {
		t.Fatalf("final replay events = job:%d chain:%d callback:%d, want 0/0/1", succeeded, completed, callbackSucceeded)
	}
}

// TestTerminalChainUnknownNodeCannotPublishFacts prevents a malformed
// delivery from borrowing either terminal outcome or its pending callback.
func TestTerminalChainUnknownNodeCannotPublishFacts(t *testing.T) {
	for _, terminal := range []string{"completed", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			chainID := "chain-runtime-unknown-" + terminal
			store := NewMemoryStore()
			if err := store.CreateChain(context.Background(), ChainRecord{
				ChainID: chainID,
				Nodes: []ChainNode{
					{NodeID: "node-0", Job: StoredJob{Type: "workflow:chain:known-0"}},
					{NodeID: "node-1", Job: StoredJob{Type: "workflow:chain:known-1"}},
				},
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if _, _, err := store.AdvanceChain(context.Background(), chainID, "node-0"); err != nil {
				t.Fatalf("advance first node: %v", err)
			}
			if terminal == "completed" {
				if _, done, err := store.AdvanceChain(context.Background(), chainID, "node-1"); err != nil || !done {
					t.Fatalf("complete chain = done:%t err:%v", done, err)
				}
			} else {
				outcomes := requireOutcomeStore(t, store)
				if _, owned, err := outcomes.FailChainNode(context.Background(), chainID, "node-1", errors.New("known failure")); err != nil || !owned {
					t.Fatalf("fail chain = owned:%t err:%v", owned, err)
				}
			}

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, callbackCalls int
			runtime.Register("workflow:chain:unknown", func(context.Context, Context) error {
				handlerCalls++
				return nil
			})
			runtime.chainCallbacks[chainID] = chainCallbacks{
				finally: func(context.Context, ChainState) error {
					callbackCalls++
					return nil
				},
			}
			err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch-runtime-unknown",
				Kind:          "chain_node",
				ChainID:       chainID,
				NodeID:        "node-missing",
				JobID:         "job-runtime-unknown",
				Job:           StoredJob{Type: "workflow:chain:unknown"},
			})
			if !busruntime.IsUncommitted(err) || !strings.Contains(err.Error(), "does not contain node") {
				t.Fatalf("unknown-node error = %v, want uncommitted membership rejection", err)
			}
			if handlerCalls != 1 || callbackCalls != 0 {
				t.Fatalf("handler/callback calls = %d/%d, want 1/0", handlerCalls, callbackCalls)
			}
			assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed)
		})
	}
}

// TestLegacyDualTerminalChainPreservesCompletion pins completion precedence
// for rows written before FailChain began protecting completed state.
func TestLegacyDualTerminalChainPreservesCompletion(t *testing.T) {
	const (
		chainID = "chain-legacy-dual-terminal"
		nodeID  = "node-legacy-dual-terminal"
		jobType = "workflow:chain:legacy-dual-terminal"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateChain(context.Background(), ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	legacyState := ChainState{
		ChainID:   chainID,
		Nodes:     []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}},
		NextIndex: 1,
		Completed: true,
		Failed:    true,
		Failure:   "late legacy failure",
	}
	faultStore := &workflowMutationFaultStore{Store: baseStore, advanceDoneWithoutState: true, getChainState: &legacyState}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)
	var handlerCalls, catchCalls, finallyCalls int
	runtime.Register(jobType, func(context.Context, Context) error { handlerCalls++; return nil })
	runtime.chainCallbacks[chainID] = chainCallbacks{
		catch:   func(context.Context, ChainState, error) error { catchCalls++; return nil },
		finally: func(context.Context, ChainState) error { finallyCalls++; return nil },
	}
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-legacy-dual-terminal",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-legacy-dual-terminal",
		Job:           StoredJob{Type: jobType},
	}); err != nil {
		t.Fatalf("handle legacy dual-terminal chain: %v", err)
	}
	if handlerCalls != 1 || catchCalls != 0 || finallyCalls != 1 {
		t.Fatalf("handler/catch/finally calls = %d/%d/%d, want 1/0/1", handlerCalls, catchCalls, finallyCalls)
	}
	var succeeded, completed int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventChainCompleted:
			completed++
		}
	}
	if succeeded != 1 || completed != 1 {
		t.Fatalf("success/completion events = %d/%d, want 1/1", succeeded, completed)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobFailed, EventChainFailed)
}

// TestFailedDuplicateCannotReplaceSuccessfulChainNode proves completion or
// advancement remains authoritative when the same physical node later fails.
func TestFailedDuplicateCannotReplaceSuccessfulChainNode(t *testing.T) {
	for _, test := range []struct {
		name          string
		nodes         []ChainNode
		wantCompleted bool
		wantNextIndex int
	}{
		{
			name:          "completed chain",
			nodes:         []ChainNode{{NodeID: "node-completed", Job: StoredJob{Type: "workflow:chain:late-failure"}}},
			wantCompleted: true,
			wantNextIndex: 1,
		},
		{
			name: "advanced chain",
			nodes: []ChainNode{
				{NodeID: "node-advanced", Job: StoredJob{Type: "workflow:chain:late-failure"}},
				{NodeID: "node-pending", Job: StoredJob{Type: "workflow:chain:pending"}},
			},
			wantNextIndex: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			const chainID = "chain-late-failure"
			applicationErr := errors.New("late duplicate failed")
			store := NewMemoryStore()
			if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, Nodes: test.nodes}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if _, _, err := store.AdvanceChain(context.Background(), chainID, test.nodes[0].NodeID); err != nil {
				t.Fatalf("commit successful node: %v", err)
			}
			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, catchCalls, finallyCalls int
			runtime.Register(test.nodes[0].Job.Type, func(context.Context, Context) error {
				handlerCalls++
				return applicationErr
			})
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
				DispatchID:    "dispatch-late-failure",
				Kind:          "chain_node",
				ChainID:       chainID,
				NodeID:        test.nodes[0].NodeID,
				JobID:         "job-late-failure",
				Job:           test.nodes[0].Job,
			})
			if err != nil {
				t.Fatalf("late failed duplicate: %v", err)
			}
			if handlerCalls != 1 || catchCalls != 0 || finallyCalls != 0 {
				t.Fatalf("handler/catch/finally calls = %d/%d/%d, want 1/0/0", handlerCalls, catchCalls, finallyCalls)
			}
			state, err := store.GetChain(context.Background(), chainID)
			if err != nil {
				t.Fatalf("get chain: %v", err)
			}
			if state.NextIndex != test.wantNextIndex || state.Completed != test.wantCompleted || state.Failed {
				t.Fatalf("chain state = %+v, want next=%d completed=%t failed=false", state, test.wantNextIndex, test.wantCompleted)
			}
			assertNoCommittedEvents(t, recorder.events, EventJobFailed, EventChainFailed, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed)
		})
	}
}

// TestBatchDuplicateCannotPublishContradictoryOutcome proves a losing physical
// result cannot emit facts, progress, or callbacks against the stored winner.
func TestBatchDuplicateCannotPublishContradictoryOutcome(t *testing.T) {
	for _, first := range []BatchJobOutcome{BatchJobSucceeded, BatchJobFailed} {
		t.Run(string(first), func(t *testing.T) {
			const (
				batchID = "batch-contradictory-outcome"
				jobID   = "job-contradictory-outcome"
				jobType = "workflow:batch:contradictory-outcome"
			)
			applicationErr := errors.New("contradictory physical failure")
			store := NewMemoryStore()
			if err := store.CreateBatch(context.Background(), BatchRecord{
				BatchID:     batchID,
				AllowFailed: true,
				Jobs:        []BatchJob{{JobID: jobID, Job: StoredJob{Type: jobType}}},
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			outcomes := requireOutcomeStore(t, store)
			before, owned, err := outcomes.SettleBatchJob(context.Background(), batchID, jobID, first, applicationErr)
			if err != nil || !owned {
				t.Fatalf("commit first outcome = owned:%t err:%v", owned, err)
			}

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, progressCalls, thenCalls, catchCalls, finallyCalls int
			runtime.Register(jobType, func(context.Context, Context) error {
				handlerCalls++
				if first == BatchJobSucceeded {
					return applicationErr
				}
				return nil
			})
			runtime.batchCallbacks[batchID] = batchCallbacks{
				progress: func(context.Context, BatchState) error { progressCalls++; return nil },
				then:     func(context.Context, BatchState) error { thenCalls++; return nil },
				catch:    func(context.Context, BatchState, error) error { catchCalls++; return nil },
				finally:  func(context.Context, BatchState) error { finallyCalls++; return nil },
			}
			err = queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobBatchJob, envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch-contradictory-outcome",
				Kind:          "batch_job",
				BatchID:       batchID,
				JobID:         jobID,
				Job:           StoredJob{Type: jobType},
			})
			if err != nil {
				t.Fatalf("contradictory duplicate: %v", err)
			}
			if handlerCalls != 1 || progressCalls != 0 || thenCalls != 0 || catchCalls != 0 || finallyCalls != 0 {
				t.Fatalf("handler/progress/then/catch/finally calls = %d/%d/%d/%d/%d, want 1/0/0/0/0", handlerCalls, progressCalls, thenCalls, catchCalls, finallyCalls)
			}
			after, err := store.GetBatch(context.Background(), batchID)
			if err != nil {
				t.Fatalf("get batch: %v", err)
			}
			if after.Pending != before.Pending || after.Processed != before.Processed || after.Failed != before.Failed || after.Cancelled != before.Cancelled || after.Completed != before.Completed {
				t.Fatalf("batch state changed: before=%+v after=%+v", before, after)
			}
			assertNoCommittedEvents(t, recorder.events,
				EventJobSucceeded,
				EventJobFailed,
				EventBatchProgressed,
				EventBatchCompleted,
				EventBatchFailed,
				EventBatchCancelled,
				EventCallbackStarted,
				EventCallbackSucceeded,
				EventCallbackFailed,
			)
		})
	}
}

// TestBatchSameOutcomeDuplicateSeparatesFactsFromCallbackRecovery proves the
// exact private claim gates logical facts while ordinary replays may still
// finish idempotently claimed compatibility callbacks.
func TestBatchSameOutcomeDuplicateSeparatesFactsFromCallbackRecovery(t *testing.T) {
	for _, test := range []struct {
		name           string
		outcome        BatchJobOutcome
		recovered      bool
		wantSucceeded  int
		wantProgressed int
		wantCompleted  int
		wantThen       int
		wantCatch      int
		wantFinally    int
		wantHandler    int
		wantPermanent  bool
	}{
		{name: "ordinary success", outcome: BatchJobSucceeded, wantThen: 1, wantFinally: 1, wantHandler: 1},
		{name: "recovered success", outcome: BatchJobSucceeded, recovered: true, wantSucceeded: 1, wantProgressed: 1, wantCompleted: 1},
		{name: "ordinary failure", outcome: BatchJobFailed, wantCatch: 1, wantFinally: 1, wantHandler: 1},
		{name: "recovered failure", outcome: BatchJobFailed, recovered: true, wantPermanent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const (
				batchID = "batch-same-outcome-duplicate"
				jobID   = "job-same-outcome-duplicate"
				jobType = "workflow:batch:same-outcome-duplicate"
			)
			store := NewMemoryStore()
			if err := store.CreateBatch(context.Background(), BatchRecord{
				BatchID:    batchID,
				DispatchID: "dispatch-same-outcome-duplicate",
				Jobs:       []BatchJob{{JobID: jobID, Job: StoredJob{Type: jobType}}},
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			delivery := envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    "dispatch-same-outcome-duplicate",
				Kind:          "batch_job",
				BatchID:       batchID,
				JobID:         jobID,
				Job:           StoredJob{Type: jobType},
			}
			settlementStore := requireBatchSettlementStore(t, store)
			applicationErr := errors.New("same outcome failure")
			seeded, err := settlementStore.settleBatchOutcome(context.Background(), batchID, jobID, test.outcome, applicationErr, workflowTransitionClaim(delivery, 2, "generation-batch-seed"))
			if err != nil || !seeded.owned || !seeded.receiptKnown {
				t.Fatalf("seed batch outcome = %+v err:%v", seeded, err)
			}

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, progressCalls, thenCalls, catchCalls, finallyCalls int
			runtime.Register(jobType, func(context.Context, Context) error {
				handlerCalls++
				if test.outcome == BatchJobFailed {
					return applicationErr
				}
				return nil
			})
			runtime.batchCallbacks[batchID] = batchCallbacks{
				progress: func(context.Context, BatchState) error { progressCalls++; return nil },
				then:     func(context.Context, BatchState) error { thenCalls++; return nil },
				catch:    func(context.Context, BatchState, error) error { catchCalls++; return nil },
				finally:  func(context.Context, BatchState) error { finallyCalls++; return nil },
			}
			deliveryContext := exhaustedWorkflowContext()
			var settlement *busruntime.DeliverySettlement
			if test.recovered {
				deliveryContext, settlement = busruntime.WithDeliverySettlement(deliveryContext)
				deliveryContext = workflowRecoveryContext(deliveryContext, "generation-batch-replay", "generation-batch-seed")
			}
			err = queueRuntime.DispatchJSON(deliveryContext, internalJobBatchJob, delivery)
			if test.wantPermanent {
				if !busruntime.IsPermanent(err) || busruntime.IsUncommitted(err) || errors.Is(err, applicationErr) {
					t.Fatalf("same-outcome duplicate error = %v, want generic permanent settlement", err)
				}
			} else if err != nil {
				t.Fatalf("same-outcome duplicate: %v", err)
			}
			if test.recovered {
				assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventBatchProgressed, EventBatchCompleted)
				settlement.Commit()
			}
			var succeeded, failed, progressed, completed, batchFailed, cancelled int
			for _, event := range recorder.events {
				switch event.Kind {
				case EventJobSucceeded:
					succeeded++
				case EventJobFailed:
					failed++
				case EventBatchProgressed:
					progressed++
				case EventBatchCompleted:
					completed++
				case EventBatchFailed:
					batchFailed++
				case EventBatchCancelled:
					cancelled++
				}
			}
			if handlerCalls != test.wantHandler || succeeded != test.wantSucceeded || progressed != test.wantProgressed || completed != test.wantCompleted || failed != 0 || batchFailed != 0 || cancelled != 0 {
				t.Fatalf("handler/job/progress/completion/failure counts = %d/%d/%d/%d/%d/%d/%d, want %d/%d/%d/%d/0/0/0", handlerCalls, succeeded, progressed, completed, failed, batchFailed, cancelled, test.wantHandler, test.wantSucceeded, test.wantProgressed, test.wantCompleted)
			}
			if progressCalls != 0 || thenCalls != test.wantThen || catchCalls != test.wantCatch || finallyCalls != test.wantFinally {
				t.Fatalf("progress/then/catch/finally calls = %d/%d/%d/%d, want 0/%d/%d/%d", progressCalls, thenCalls, catchCalls, finallyCalls, test.wantThen, test.wantCatch, test.wantFinally)
			}
		})
	}
}

// TestBatchRecoverySettlesNonFactOwnersWithoutFacts proves a valid member
// receipt settles physical nonowners without granting them fact ownership.
func TestBatchRecoverySettlesNonFactOwnersWithoutFacts(t *testing.T) {
	const owner = "generation-batch-non-fact-owner"
	recoveryCases := []struct {
		name                  string
		attempt               int
		recoveredGenerationID string
	}{
		{name: "different physical attempt", attempt: 3, recoveredGenerationID: owner},
		{name: "negative current attempt", attempt: -1, recoveredGenerationID: owner},
		{name: "different recovered generation", attempt: 2, recoveredGenerationID: "generation-batch-different-recovered"},
		{name: "legacy recovery without generation", attempt: 2},
	}
	for _, receiptOutcome := range []BatchJobOutcome{BatchJobSucceeded, BatchJobFailed} {
		for _, recoveryCase := range recoveryCases {
			t.Run(string(receiptOutcome)+"/"+recoveryCase.name, func(t *testing.T) {
				const (
					batchID    = "batch-non-fact-owner"
					dispatchID = "dispatch-batch-non-fact-owner"
					jobID      = "job-batch-non-fact-owner"
					jobType    = "workflow:batch:non-fact-owner"
				)
				store := NewMemoryStore()
				env := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "batch_job", BatchID: batchID, JobID: jobID, Job: StoredJob{Type: jobType, Payload: []byte(`{"id":5}`)}}
				if err := store.CreateBatch(context.Background(), BatchRecord{BatchID: batchID, DispatchID: dispatchID, AllowFailed: true, Jobs: []BatchJob{{JobID: jobID, Job: env.Job}}}); err != nil {
					t.Fatalf("create batch: %v", err)
				}
				applicationCause := errors.New("original batch member failure")
				settled, err := requireBatchSettlementStore(t, store).settleBatchOutcome(context.Background(), batchID, jobID, receiptOutcome, applicationCause, workflowTransitionClaim(env, 2, owner))
				if err != nil || !settled.claimedNow || !settled.receiptKnown || !settled.state.Completed {
					t.Fatalf("commit batch member = %+v err:%v", settled, err)
				}

				runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
				var handlerCalls, callbackCalls int
				runtime.Register(jobType, func(context.Context, Context) error {
					handlerCalls++
					return applicationCause
				})
				runtime.batchCallbacks[batchID] = batchCallbacks{
					progress: func(context.Context, BatchState) error {
						callbackCalls++
						return nil
					},
					then: func(context.Context, BatchState) error {
						callbackCalls++
						return nil
					},
					catch: func(context.Context, BatchState, error) error {
						callbackCalls++
						return nil
					},
					finally: func(context.Context, BatchState) error {
						callbackCalls++
						return nil
					},
				}
				attemptContext := busruntime.WithDeliveryAttempt(context.Background(), busruntime.DeliveryAttempt{Number: recoveryCase.attempt, MaxRetry: 3})
				recoveryContext, deliverySettlement := busruntime.WithDeliverySettlement(attemptContext)
				recoveryContext = workflowRecoveryContext(recoveryContext, "generation-batch-non-fact-owner-current", recoveryCase.recoveredGenerationID)
				recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobBatchJob, env)
				if receiptOutcome == BatchJobFailed {
					if recoveryErr == nil || !busruntime.IsPermanent(recoveryErr) || busruntime.IsUncommitted(recoveryErr) || errors.Is(recoveryErr, applicationCause) {
						t.Fatalf("failed-member nonowner recovery = %v, want generic permanent", recoveryErr)
					}
				} else if recoveryErr != nil {
					t.Fatalf("successful-member nonowner recovery: %v", recoveryErr)
				}
				deliverySettlement.Commit()
				if handlerCalls != 0 || callbackCalls != 0 || deliverySettlement.ApplicationStateCommitted() || len(recorder.events) != 0 {
					t.Fatalf("handler/callback/committed/events = %d/%d/%t/%d, want 0/0/false/0", handlerCalls, callbackCalls, deliverySettlement.ApplicationStateCommitted(), len(recorder.events))
				}
			})
		}
	}
}

// TestBatchNonterminalReceiptCannotReplayLaterCompletion proves an ordinary
// duplicate of an earlier member cannot run terminal callbacks merely because
// another member completed the aggregate before its receipt was re-read.
func TestBatchNonterminalReceiptCannotReplayLaterCompletion(t *testing.T) {
	const (
		batchID      = "batch-nonterminal-receipt-replay"
		dispatchID   = "dispatch-nonterminal-receipt-replay"
		firstJobID   = "job-nonterminal-receipt-replay"
		finalJobID   = "job-terminal-receipt-owner"
		firstJobType = "workflow:batch:nonterminal-receipt-replay"
	)
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID:     batchID,
		DispatchID:  dispatchID,
		AllowFailed: true,
		Jobs: []BatchJob{
			{JobID: firstJobID, Job: StoredJob{Type: firstJobType}},
			{JobID: finalJobID, Job: StoredJob{Type: "workflow:batch:terminal-receipt-owner"}},
		},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	settlements := requireBatchSettlementStore(t, store)
	first := envelope{DispatchID: dispatchID, BatchID: batchID, JobID: firstJobID, Job: StoredJob{Type: firstJobType}}
	final := envelope{DispatchID: dispatchID, BatchID: batchID, JobID: finalJobID, Job: StoredJob{Type: "workflow:batch:terminal-receipt-owner"}}
	firstResult, err := settlements.settleBatchOutcome(context.Background(), batchID, firstJobID, BatchJobSucceeded, nil, workflowTransitionClaim(first, 0, "generation-nonterminal-receipt"))
	if err != nil || !firstResult.claimedNow || firstResult.state.Completed || firstResult.receipt.aggregateCompleted {
		t.Fatalf("settle nonterminal member = %+v, err:%v", firstResult, err)
	}
	finalResult, err := settlements.settleBatchOutcome(context.Background(), batchID, finalJobID, BatchJobSucceeded, nil, workflowTransitionClaim(final, 0, "generation-terminal-receipt"))
	if err != nil || !finalResult.claimedNow || !finalResult.state.Completed || !finalResult.receipt.aggregateCompleted {
		t.Fatalf("settle terminal member = %+v, err:%v", finalResult, err)
	}

	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls, thenCalls, finallyCalls int
	runtime.Register(firstJobType, func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	runtime.batchCallbacks[batchID] = batchCallbacks{
		then:    func(context.Context, BatchState) error { thenCalls++; return nil },
		finally: func(context.Context, BatchState) error { finallyCalls++; return nil },
	}
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobBatchJob, first); err != nil {
		t.Fatalf("replay nonterminal member: %v", err)
	}
	if handlerCalls != 1 || thenCalls != 0 || finallyCalls != 0 {
		t.Fatalf("handler/then/finally calls = %d/%d/%d, want 1/0/0", handlerCalls, thenCalls, finallyCalls)
	}
	assertNoCommittedEvents(t, recorder.events, EventBatchCompleted, EventBatchFailed, EventBatchCancelled, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed)
}

// TestBatchSettlementPublicOutcomeFallbackPreservesCompatibility proves a
// custom additive store retains its established category ownership even though
// the public interface cannot expose an exact per-call claim result.
func TestBatchSettlementPublicOutcomeFallbackPreservesCompatibility(t *testing.T) {
	const (
		batchID = "batch-public-outcome-fallback"
		jobID   = "job-public-outcome-fallback"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateBatch(context.Background(), BatchRecord{
		BatchID: batchID,
		Jobs:    []BatchJob{{JobID: jobID}},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	store := compatibilityOutcomeStore{Store: baseStore}
	runtime, _, _ := newWorkflowMutationRuntime(t, store)
	first, err := runtime.settleBatchJob(context.Background(), batchID, jobID, BatchJobSucceeded, nil, transitionClaim{})
	if err != nil || !first.owned || !first.claimedNow || first.state.Processed != 1 {
		t.Fatalf("first settlement = %+v err:%v, want owned compatibility claim", first, err)
	}
	replayed, err := runtime.settleBatchJob(context.Background(), batchID, jobID, BatchJobSucceeded, nil, transitionClaim{})
	if err != nil || !replayed.owned || !replayed.claimedNow || replayed.state.Processed != 1 {
		t.Fatalf("replayed settlement = %+v err:%v, want projected compatibility claim", replayed, err)
	}
	contradictory, err := runtime.settleBatchJob(context.Background(), batchID, jobID, BatchJobFailed, errors.New("contradictory"), transitionClaim{})
	if err != nil || contradictory.owned || !contradictory.claimedNow || contradictory.state.Processed != 1 {
		t.Fatalf("contradictory settlement = %+v err:%v, want losing projected compatibility claim", contradictory, err)
	}
}

// TestUnknownBatchMemberStopsBeforeExecution prevents malformed workflow
// correlation from running a handler or mutating the real aggregate.
func TestUnknownBatchMemberStopsBeforeExecution(t *testing.T) {
	const (
		batchID = "batch-unknown-member"
		jobType = "workflow:batch:unknown-member"
	)
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID: batchID,
		Jobs:    []BatchJob{{JobID: "job-known"}},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls, progressCalls, finallyCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	runtime.batchCallbacks[batchID] = batchCallbacks{
		progress: func(context.Context, BatchState) error { progressCalls++; return nil },
		finally:  func(context.Context, BatchState) error { finallyCalls++; return nil },
	}
	err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobBatchJob, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-unknown-member",
		Kind:          "batch_job",
		BatchID:       batchID,
		JobID:         "job-missing",
		Job:           StoredJob{Type: jobType},
	})
	if !busruntime.IsUncommitted(err) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown member error = %v, want uncommitted ErrNotFound", err)
	}
	if handlerCalls != 0 || progressCalls != 0 || finallyCalls != 0 {
		t.Fatalf("handler/progress/finally calls = %d/%d/%d, want 0/0/0", handlerCalls, progressCalls, finallyCalls)
	}
	state, stateErr := store.GetBatch(context.Background(), batchID)
	if stateErr != nil {
		t.Fatalf("get batch: %v", stateErr)
	}
	if state.Pending != 1 || state.Processed != 0 || state.Failed != 0 || state.Completed || state.Cancelled {
		t.Fatalf("unknown member changed batch: %+v", state)
	}
	assertNoCommittedEvents(t, recorder.events,
		EventJobStarted,
		EventJobSucceeded,
		EventJobFailed,
		EventBatchProgressed,
		EventBatchCompleted,
		EventBatchFailed,
		EventBatchCancelled,
		EventCallbackStarted,
		EventCallbackSucceeded,
		EventCallbackFailed,
	)
}

// TestChainCompletionReadFailureRedeliversWithoutFacts proves a committed
// terminal mutation is replayed until its state can be confirmed for events.
func TestChainCompletionReadFailureRedeliversWithoutFacts(t *testing.T) {
	storeErr := errors.New("chain completion read unavailable")
	const (
		chainID = "chain-completion-read-failure"
		nodeID  = "node-completion-read-failure"
		jobType = "workflow:chain:completion-read-failure"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes:   []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	faultStore := &workflowMutationFaultStore{Store: baseStore, getChainErr: storeErr}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)
	var handlerCalls, finallyCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	runtime.chainCallbacks[chainID] = chainCallbacks{
		finally: func(context.Context, ChainState) error {
			finallyCalls++
			return nil
		},
	}
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-completion-read-failure",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-completion-read-failure",
		Job:           StoredJob{Type: jobType},
	}
	err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, delivery)
	assertUncommittedMutation(t, err, storeErr)
	if handlerCalls != 1 || finallyCalls != 0 {
		t.Fatalf("handler/finally calls before recovery = %d/%d, want 1/0", handlerCalls, finallyCalls)
	}
	state, err := baseStore.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get committed chain: %v", err)
	}
	if !state.Completed || state.Failed {
		t.Fatalf("committed chain state = %+v, want completed only", state)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainCompleted, EventCallbackStarted, EventCallbackSucceeded)

	faultStore.getChainErr = nil
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, delivery); err != nil {
		t.Fatalf("redeliver after store recovery: %v", err)
	}
	if handlerCalls != 2 || finallyCalls != 1 {
		t.Fatalf("handler/finally calls after recovery = %d/%d, want 2/1", handlerCalls, finallyCalls)
	}
	var succeeded, completed, callbackSucceeded int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventChainCompleted:
			completed++
		case EventCallbackSucceeded:
			callbackSucceeded++
		}
	}
	if succeeded != 1 || completed != 1 || callbackSucceeded != 1 {
		t.Fatalf("job/chain/callback success events = %d/%d/%d, want 1/1/1", succeeded, completed, callbackSucceeded)
	}
}

// TestChainCommittedSuccessSurvivesContradictorySettlementReplay proves a
// failed physical settlement recovers receipt-owned terminal facts without
// re-executing application code that could produce a contradictory result.
func TestChainCommittedSuccessSurvivesContradictorySettlementReplay(t *testing.T) {
	const (
		chainID = "chain-committed-success-settlement-replay"
		nodeID  = "node-committed-success-settlement-replay"
		jobType = "workflow:chain:committed-success-settlement-replay"
	)
	store := NewMemoryStore()
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID:    chainID,
		DispatchID: "dispatch-committed-success-settlement-replay",
		Nodes:      []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		if handlerCalls == 1 {
			return nil
		}
		return busruntime.Permanent(errors.New("contradictory replay failure"))
	})
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-committed-success-settlement-replay",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-committed-success-settlement-replay",
		Job:           StoredJob{Type: jobType},
	}

	firstContext, _ := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	firstContext = workflowGenerationContext(firstContext, "generation-chain-committed-success")
	if err := queueRuntime.DispatchJSON(firstContext, internalJobChainNode, delivery); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainCompleted)
	state, err := store.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get committed chain: %v", err)
	}
	if !state.Completed || state.Failed {
		t.Fatalf("committed chain state = %+v, want completed only", state)
	}

	replayContext, replaySettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	replayContext = workflowRecoveryContext(replayContext, "generation-chain-replay", "generation-chain-committed-success")
	if err := queueRuntime.DispatchJSON(replayContext, internalJobChainNode, delivery); err != nil {
		t.Fatalf("contradictory redelivery: %v", err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainCompleted)
	replaySettlement.Commit()

	var succeeded, completed, failed int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventChainCompleted:
			completed++
		case EventJobFailed, EventChainFailed:
			failed++
		}
	}
	if handlerCalls != 1 || succeeded != 1 || completed != 1 || failed != 0 {
		t.Fatalf("handler/job/chain/failure counts = %d/%d/%d/%d, want 1/1/1/0", handlerCalls, succeeded, completed, failed)
	}
}

// TestChainPostTransitionFailureMarksCurrentGenerationForRecovery proves a
// recovered delivery that becomes the transition owner does not retain an
// older generation when successor enqueue requires same-attempt redelivery.
func TestChainPostTransitionFailureMarksCurrentGenerationForRecovery(t *testing.T) {
	const (
		chainID      = "chain-post-transition-generation"
		dispatchID   = "dispatch-post-transition-generation"
		firstNodeID  = "node-post-transition-generation"
		finalNodeID  = "node-post-transition-generation-final"
		firstJobType = "workflow:chain:post-transition-generation"
	)
	store := NewMemoryStore()
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID:    chainID,
		DispatchID: dispatchID,
		Nodes: []ChainNode{
			{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType}},
			{NodeID: finalNodeID, Job: StoredJob{Type: "workflow:chain:post-transition-generation:final"}},
		},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls int
	runtime.Register(firstJobType, func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        firstNodeID,
		JobID:         "job-post-transition-generation",
		Job:           StoredJob{Type: firstJobType},
	}
	payload, err := json.Marshal(delivery)
	if err != nil {
		t.Fatalf("encode delivery: %v", err)
	}
	successorErr := errors.New("successor queue unavailable")
	queueRuntime.dispatchErr = successorErr
	deliveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	deliveryContext = workflowRecoveryContext(deliveryContext, "generation-post-transition-current", "generation-post-transition-older")
	handler := queueRuntime.handlers[internalJobChainNode]
	if handler == nil {
		t.Fatal("chain delivery handler is not registered")
	}
	err = handler(deliveryContext, testInboundJob{payload: payload})
	if !busruntime.IsUncommitted(err) || !errors.Is(err, successorErr) {
		t.Fatalf("post-transition error = %v, want uncommitted successor error", err)
	}
	if handlerCalls != 1 || !settlement.ApplicationStateCommitted() {
		t.Fatalf("handler calls/application-state signal = %d/%t, want 1/true", handlerCalls, settlement.ApplicationStateCommitted())
	}
	receipt, known, err := requireTransitionReceiptStore(t, store).chainTransitionReceipt(context.Background(), chainID, firstNodeID)
	if err != nil || !known || receipt.owner.deliveryID != "generation-post-transition-current" {
		t.Fatalf("post-transition receipt = known:%t receipt:%+v err:%v", known, receipt, err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced)
}

// TestChainRecoveryWithoutExactReceiptOwnershipPreservesOnlyLiveContinuation
// proves compatibility recovery restores liveness without replaying application effects.
func TestChainRecoveryWithoutExactReceiptOwnershipPreservesOnlyLiveContinuation(t *testing.T) {
	const (
		chainID       = "chain-recovery-without-exact-receipt"
		dispatchID    = "dispatch-recovery-without-exact-receipt"
		firstNodeID   = "node-recovery-without-exact-receipt-first"
		secondNodeID  = "node-recovery-without-exact-receipt-second"
		finalNodeID   = "node-recovery-without-exact-receipt-final"
		firstJobType  = "workflow:chain:recovery-without-exact-receipt:first"
		secondJobType = "workflow:chain:recovery-without-exact-receipt:second"
		finalJobType  = "workflow:chain:recovery-without-exact-receipt:final"
	)
	tests := []struct {
		name            string
		decorateStore   bool
		receiptOwner    string
		recoveredOwner  string
		advances        int
		fail            bool
		wantSuccessor   bool
		rejectSuccessor bool
	}{
		{name: "memory missing receipt live successor", recoveredOwner: "generation-unrecorded", advances: 1, wantSuccessor: true},
		{name: "memory missing receipt rejected successor", recoveredOwner: "generation-unrecorded", advances: 1, wantSuccessor: true, rejectSuccessor: true},
		{name: "memory missing receipt progressed successor", recoveredOwner: "generation-unrecorded", advances: 2},
		{name: "memory missing receipt completed chain", recoveredOwner: "generation-unrecorded", advances: 3},
		{name: "memory missing receipt failed chain", recoveredOwner: "generation-unrecorded", advances: 1, fail: true},
		{name: "decorated store live successor", decorateStore: true, recoveredOwner: "generation-unrecorded", advances: 1, wantSuccessor: true},
		{name: "decorated store progressed successor", decorateStore: true, recoveredOwner: "generation-unrecorded", advances: 2},
		{name: "decorated store completed chain", decorateStore: true, recoveredOwner: "generation-unrecorded", advances: 3},
		{name: "decorated store failed chain", decorateStore: true, recoveredOwner: "generation-unrecorded", advances: 1, fail: true},
		{name: "supported receipt different recovered generation", receiptOwner: "generation-receipt-owner", recoveredOwner: "generation-different", advances: 1, wantSuccessor: true},
		{name: "supported receipt legacy recovery without generation", receiptOwner: "generation-receipt-owner", advances: 1, wantSuccessor: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseStore := NewMemoryStore()
			nodes := []ChainNode{
				{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType}},
				{NodeID: secondNodeID, Job: StoredJob{Type: secondJobType}},
				{NodeID: finalNodeID, Job: StoredJob{Type: finalJobType}},
			}
			if err := baseStore.CreateChain(context.Background(), ChainRecord{
				ChainID:    chainID,
				DispatchID: dispatchID,
				Nodes:      nodes,
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			delivery := envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    dispatchID,
				Kind:          "chain_node",
				ChainID:       chainID,
				NodeID:        firstNodeID,
				JobID:         "job-recovery-without-exact-receipt",
				Job:           nodes[0].Job,
			}
			if test.receiptOwner != "" {
				advanced, err := requireChainAdvanceStore(t, baseStore).advanceChainOutcome(
					context.Background(),
					chainID,
					firstNodeID,
					workflowTransitionClaim(delivery, 2, test.receiptOwner),
				)
				if err != nil || !advanced.claimedNow || advanced.done || advanced.next == nil || advanced.next.NodeID != secondNodeID {
					t.Fatalf("advance receipt owner = %+v, err:%v", advanced, err)
				}
			} else {
				next, done, err := baseStore.AdvanceChain(context.Background(), chainID, firstNodeID)
				if err != nil || done || next == nil || next.NodeID != secondNodeID {
					t.Fatalf("advance receiptless predecessor = next:%+v done:%t err:%v", next, done, err)
				}
			}
			for index := 1; index < test.advances; index++ {
				next, done, err := baseStore.AdvanceChain(context.Background(), chainID, nodes[index].NodeID)
				if err != nil {
					t.Fatalf("advance node %d: %v", index, err)
				}
				if index == len(nodes)-1 {
					if !done || next != nil {
						t.Fatalf("complete chain = next:%+v done:%t, want nil/true", next, done)
					}
					continue
				}
				if done || next == nil || next.NodeID != nodes[index+1].NodeID {
					t.Fatalf("advance node %d = next:%+v done:%t", index, next, done)
				}
			}
			if test.fail {
				if err := baseStore.FailChain(context.Background(), chainID, errors.New("committed chain failure")); err != nil {
					t.Fatalf("fail chain: %v", err)
				}
			}

			receipt, receiptKnown, err := requireTransitionReceiptStore(t, baseStore).chainTransitionReceipt(context.Background(), chainID, firstNodeID)
			if err != nil {
				t.Fatalf("read predecessor receipt: %v", err)
			}
			if test.receiptOwner == "" && receiptKnown {
				t.Fatalf("receiptless predecessor unexpectedly persisted receipt %+v", receipt)
			}
			if test.receiptOwner != "" && (!receiptKnown || receipt.owner.deliveryID != test.receiptOwner) {
				t.Fatalf("predecessor receipt = known:%t receipt:%+v", receiptKnown, receipt)
			}

			var runtimeStore Store = baseStore
			if test.decorateStore {
				runtimeStore = &workflowMutationFaultStore{Store: baseStore}
				if _, capable := runtimeStore.(transitionReceiptStore); capable {
					t.Fatal("decorated compatibility store unexpectedly exposes transition receipts")
				}
			}
			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, runtimeStore)
			var applicationCalls, callbackCalls int
			for _, jobType := range []string{firstJobType, secondJobType, finalJobType} {
				runtime.Register(jobType, func(context.Context, Context) error {
					applicationCalls++
					return nil
				})
			}
			runtime.chainCallbacks[chainID] = chainCallbacks{
				catch: func(context.Context, ChainState, error) error {
					callbackCalls++
					return nil
				},
				finally: func(context.Context, ChainState) error {
					callbackCalls++
					return nil
				},
			}
			payload, err := json.Marshal(delivery)
			if err != nil {
				t.Fatalf("encode predecessor delivery: %v", err)
			}
			handler := queueRuntime.handlers[internalJobChainNode]
			if handler == nil {
				t.Fatal("chain delivery handler is not registered")
			}
			var successors []envelope
			queueRuntime.handlers[internalJobChainNode] = func(_ context.Context, job busruntime.InboundJob) error {
				var successor envelope
				if err := job.Bind(&successor); err != nil {
					return err
				}
				successors = append(successors, successor)
				return nil
			}
			var successorErr error
			if test.rejectSuccessor {
				successorErr = errors.New("receiptless successor enqueue rejected")
				queueRuntime.dispatchErr = successorErr
			}
			recoveryContext := workflowRecoveryContext(exhaustedWorkflowContext(), "generation-recovery-current", test.recoveredOwner)
			recoveryErr := handler(recoveryContext, testInboundJob{payload: payload})
			if test.rejectSuccessor {
				assertUncommittedMutation(t, recoveryErr, successorErr)
			} else if recoveryErr != nil {
				t.Fatalf("recover predecessor: %v", recoveryErr)
			}

			wantSuccessors := 0
			if test.wantSuccessor && !test.rejectSuccessor {
				wantSuccessors = 1
			}
			if len(successors) != wantSuccessors {
				t.Fatalf("successor dispatches = %d, want %d", len(successors), wantSuccessors)
			}
			if test.wantSuccessor && !test.rejectSuccessor {
				successor := successors[0]
				if successor.ChainID != chainID || successor.DispatchID != dispatchID || successor.NodeID != secondNodeID || successor.Job.Type != secondJobType || successor.JobID == "" {
					t.Fatalf("successor envelope = %+v, want immediate live successor", successor)
				}
			}
			if applicationCalls != 0 || callbackCalls != 0 || len(recorder.events) != 0 {
				t.Fatalf("application/callback/event counts = %d/%d/%d, want 0/0/0", applicationCalls, callbackCalls, len(recorder.events))
			}
			state, err := baseStore.GetChain(context.Background(), chainID)
			if err != nil {
				t.Fatalf("get recovered chain: %v", err)
			}
			if state.NextIndex != test.advances || state.Completed != (test.advances == len(nodes)) || state.Failed != test.fail {
				t.Fatalf("chain state after recovery = %+v", state)
			}
		})
	}
}

// TestChainSuccessRecoveryAllowsDifferentPhysicalDeliveryIdentity proves
// duplicate chain rows restore only liveness when another job or attempt owns facts.
func TestChainSuccessRecoveryAllowsDifferentPhysicalDeliveryIdentity(t *testing.T) {
	for _, test := range []struct {
		name           string
		currentAttempt int
		currentJobID   string
	}{
		{name: "different physical job", currentAttempt: 2, currentJobID: "job-chain-success-duplicate"},
		{name: "different physical attempt", currentAttempt: 3, currentJobID: "job-chain-success-owner"},
		{name: "negative current attempt", currentAttempt: -1, currentJobID: "job-chain-success-owner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const (
				chainID      = "chain-success-physical-nonowner"
				dispatchID   = "dispatch-chain-success-physical-nonowner"
				firstNodeID  = "node-chain-success-physical-owner"
				secondNodeID = "node-chain-success-physical-next"
				firstJobType = "workflow:chain:success-physical-owner"
				nextJobType  = "workflow:chain:success-physical-next"
				owner        = "generation-chain-success-physical-owner"
			)
			store := NewMemoryStore()
			nodes := []ChainNode{
				{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType, Payload: []byte(`{"id":4}`)}},
				{NodeID: secondNodeID, Job: StoredJob{Type: nextJobType}},
			}
			if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: nodes}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			ownerEnv := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "chain_node", ChainID: chainID, NodeID: firstNodeID, JobID: "job-chain-success-owner", Job: nodes[0].Job}
			advanced, err := requireChainAdvanceStore(t, store).advanceChainOutcome(context.Background(), chainID, firstNodeID, workflowTransitionClaim(ownerEnv, 2, owner))
			if err != nil || !advanced.claimedNow || advanced.done || advanced.next == nil || advanced.next.NodeID != secondNodeID {
				t.Fatalf("commit predecessor success = %+v err:%v", advanced, err)
			}

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, callbackCalls int
			for _, jobType := range []string{firstJobType, nextJobType} {
				runtime.Register(jobType, func(context.Context, Context) error {
					handlerCalls++
					return nil
				})
			}
			runtime.chainCallbacks[chainID] = chainCallbacks{finally: func(context.Context, ChainState) error {
				callbackCalls++
				return nil
			}}
			payloadEnv := ownerEnv
			payloadEnv.JobID = test.currentJobID
			payload, err := json.Marshal(payloadEnv)
			if err != nil {
				t.Fatalf("encode duplicate predecessor: %v", err)
			}
			handler := queueRuntime.handlers[internalJobChainNode]
			if handler == nil {
				t.Fatal("chain delivery handler is not registered")
			}
			var successors []envelope
			queueRuntime.handlers[internalJobChainNode] = func(_ context.Context, job busruntime.InboundJob) error {
				var successor envelope
				if err := job.Bind(&successor); err != nil {
					return err
				}
				successors = append(successors, successor)
				return nil
			}
			attemptContext := busruntime.WithDeliveryAttempt(context.Background(), busruntime.DeliveryAttempt{Number: test.currentAttempt, MaxRetry: 3})
			recoveryContext, settlement := busruntime.WithDeliverySettlement(attemptContext)
			recoveryContext = workflowRecoveryContext(recoveryContext, "generation-chain-success-current", owner)
			if err := handler(recoveryContext, testInboundJob{payload: payload}); err != nil {
				t.Fatalf("recover physical nonowner predecessor: %v", err)
			}
			settlement.Commit()
			if len(successors) != 1 || successors[0].NodeID != secondNodeID || successors[0].Job.Type != nextJobType || successors[0].JobID == "" {
				t.Fatalf("successor dispatches = %+v, want one immediate successor", successors)
			}
			if handlerCalls != 0 || callbackCalls != 0 || settlement.ApplicationStateCommitted() || len(recorder.events) != 0 {
				t.Fatalf("handler/callback/committed/events = %d/%d/%t/%d, want 0/0/false/0", handlerCalls, callbackCalls, settlement.ApplicationStateCommitted(), len(recorder.events))
			}
		})
	}
}

// TestChainSuccessRecoveryRejectsInvalidReceiptShapeBeforeLiveness proves a
// malformed supported receipt cannot emit facts or restore a continuation.
func TestChainSuccessRecoveryRejectsInvalidReceiptShapeBeforeLiveness(t *testing.T) {
	const (
		chainID       = "chain-invalid-success-receipt-shape"
		dispatchID    = "dispatch-invalid-success-receipt-shape"
		firstNodeID   = "node-invalid-success-receipt-shape-first"
		secondNodeID  = "node-invalid-success-receipt-shape-second"
		firstJobType  = "workflow:chain:invalid-success-receipt-shape:first"
		secondJobType = "workflow:chain:invalid-success-receipt-shape:second"
		owner         = "generation-invalid-success-receipt-shape"
	)
	tests := []struct {
		name           string
		final          bool
		recoveredOwner string
		diagnostic     string
		mutate         func(*transitionReceipt)
	}{
		{
			name:           "nonfinal completion exact owner",
			recoveredOwner: owner,
			diagnostic:     "completion",
			mutate: func(receipt *transitionReceipt) {
				receipt.aggregateCompleted = true
			},
		},
		{
			name:           "nonfinal cancellation different owner",
			recoveredOwner: "generation-different-owner",
			diagnostic:     "cancellation",
			mutate: func(receipt *transitionReceipt) {
				receipt.aggregateCancelled = true
			},
		},
		{
			name:       "final missing completion legacy recovery",
			final:      true,
			diagnostic: "completion",
			mutate: func(receipt *transitionReceipt) {
				receipt.aggregateCompleted = false
			},
		},
		{
			name:           "final cancellation different owner",
			final:          true,
			recoveredOwner: "generation-different-owner",
			diagnostic:     "cancellation",
			mutate: func(receipt *transitionReceipt) {
				receipt.aggregateCancelled = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore().(*memoryStore)
			nodes := []ChainNode{{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType}}}
			if !test.final {
				nodes = append(nodes, ChainNode{NodeID: secondNodeID, Job: StoredJob{Type: secondJobType}})
			}
			if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: nodes}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			delivery := envelope{
				SchemaVersion: schemaVersion,
				DispatchID:    dispatchID,
				Kind:          "chain_node",
				ChainID:       chainID,
				NodeID:        firstNodeID,
				JobID:         "job-invalid-success-receipt-shape",
				Job:           nodes[0].Job,
			}
			advanced, err := store.advanceChainOutcome(context.Background(), chainID, firstNodeID, workflowTransitionClaim(delivery, 2, owner))
			if err != nil || !advanced.claimedNow || !advanced.receiptKnown || advanced.done != test.final {
				t.Fatalf("advance chain = %+v err:%v", advanced, err)
			}
			key := transitionReceiptKey{workflowKind: chainTransitionKind, workflowID: chainID, memberID: firstNodeID}
			store.mu.Lock()
			receipt := store.transitionReceipts[key]
			test.mutate(&receipt)
			store.transitionReceipts[key] = receipt
			store.mu.Unlock()

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var applicationCalls, callbackCalls, successorDispatches int
			for _, jobType := range []string{firstJobType, secondJobType} {
				runtime.Register(jobType, func(context.Context, Context) error {
					applicationCalls++
					return nil
				})
			}
			runtime.chainCallbacks[chainID] = chainCallbacks{
				catch: func(context.Context, ChainState, error) error {
					callbackCalls++
					return nil
				},
				finally: func(context.Context, ChainState) error {
					callbackCalls++
					return nil
				},
			}
			payload, err := json.Marshal(delivery)
			if err != nil {
				t.Fatalf("encode delivery: %v", err)
			}
			handler := queueRuntime.handlers[internalJobChainNode]
			if handler == nil {
				t.Fatal("chain delivery handler is not registered")
			}
			queueRuntime.handlers[internalJobChainNode] = func(context.Context, busruntime.InboundJob) error {
				successorDispatches++
				return nil
			}
			recoveryContext := workflowRecoveryContext(exhaustedWorkflowContext(), "generation-invalid-shape-current", test.recoveredOwner)
			recoveryErr := handler(recoveryContext, testInboundJob{payload: payload})
			if !busruntime.IsUncommitted(recoveryErr) || !strings.Contains(recoveryErr.Error(), test.diagnostic) {
				t.Fatalf("invalid receipt recovery error = %v, want uncommitted %q diagnostic", recoveryErr, test.diagnostic)
			}
			if applicationCalls != 0 || callbackCalls != 0 || successorDispatches != 0 || len(recorder.events) != 0 {
				t.Fatalf("application/callback/successor/event counts = %d/%d/%d/%d, want 0/0/0/0", applicationCalls, callbackCalls, successorDispatches, len(recorder.events))
			}
		})
	}
}

// TestChainRecoveredPredecessorDoesNotRedispatchAfterTerminalSuccessor proves
// a stale parent can release its own winner facts without repeating terminal work.
func TestChainRecoveredPredecessorDoesNotRedispatchAfterTerminalSuccessor(t *testing.T) {
	const (
		chainID      = "chain-recovered-predecessor"
		firstNodeID  = "node-recovered-predecessor"
		finalNodeID  = "node-recovered-final"
		firstJobType = "workflow:chain:recovered-predecessor"
		finalJobType = "workflow:chain:recovered-final"
	)
	store := NewMemoryStore()
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID:    chainID,
		DispatchID: "dispatch-recovered-predecessor",
		Nodes: []ChainNode{
			{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType}},
			{NodeID: finalNodeID, Job: StoredJob{Type: finalJobType}},
		},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-recovered-predecessor",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        firstNodeID,
		JobID:         "job-recovered-predecessor",
		Job:           StoredJob{Type: firstJobType},
	}
	if advanced, err := requireChainAdvanceStore(t, store).advanceChainOutcome(context.Background(), chainID, firstNodeID, workflowTransitionClaim(delivery, 2, "generation-chain-predecessor")); err != nil || !advanced.claimedNow {
		t.Fatalf("advance predecessor = %+v, err:%v", advanced, err)
	}
	if _, done, err := store.AdvanceChain(context.Background(), chainID, finalNodeID); err != nil || !done {
		t.Fatalf("complete successor = done:%t err:%v", done, err)
	}

	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var firstCalls, finalCalls, finallyCalls int
	runtime.Register(firstJobType, func(context.Context, Context) error {
		firstCalls++
		return busruntime.Permanent(errors.New("contradictory predecessor failure"))
	})
	runtime.Register(finalJobType, func(context.Context, Context) error {
		finalCalls++
		return nil
	})
	runtime.chainCallbacks[chainID] = chainCallbacks{
		finally: func(context.Context, ChainState) error {
			finallyCalls++
			return nil
		},
	}
	deliveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	deliveryContext = workflowRecoveryContext(deliveryContext, "generation-chain-predecessor-replay", "generation-chain-predecessor")
	if err := queueRuntime.DispatchJSON(deliveryContext, internalJobChainNode, delivery); err != nil {
		t.Fatalf("recover predecessor: %v", err)
	}
	if firstCalls != 0 || finalCalls != 0 || finallyCalls != 0 {
		t.Fatalf("handler/callback calls = first:%d final:%d finally:%d, want 0/0/0", firstCalls, finalCalls, finallyCalls)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)
	settlement.Commit()

	var succeeded, advanced, completed, failed int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventChainAdvanced:
			advanced++
		case EventChainCompleted:
			completed++
		case EventJobFailed, EventChainFailed:
			failed++
		}
	}
	if succeeded != 1 || advanced != 1 || completed != 0 || failed != 0 {
		t.Fatalf("job/advance/completion/failure counts = %d/%d/%d/%d, want 1/1/0/0", succeeded, advanced, completed, failed)
	}
}

// TestChainSuccessorRejectionRecoversWithoutPredecessorReplay proves exact
// receipt recovery restores a continuation stranded by definite rejection.
func TestChainSuccessorRejectionRecoversWithoutPredecessorReplay(t *testing.T) {
	const (
		chainID      = "chain-recovered-successful-predecessor"
		dispatchID   = "dispatch-recovered-successful-predecessor"
		firstNodeID  = "node-recovered-successful-predecessor"
		finalNodeID  = "node-recovered-successful-final"
		firstJobType = "workflow:chain:recovered-successful-predecessor"
		finalJobType = "workflow:chain:recovered-successful-final"
		owner        = "generation-chain-successful-predecessor"
	)
	store := NewMemoryStore()
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID:    chainID,
		DispatchID: dispatchID,
		Nodes: []ChainNode{
			{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType}},
			{NodeID: finalNodeID, Job: StoredJob{Type: finalJobType}},
		},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        firstNodeID,
		JobID:         "job-recovered-successful-predecessor",
		Job:           StoredJob{Type: firstJobType},
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var firstCalls, finalCalls int
	runtime.Register(firstJobType, func(context.Context, Context) error {
		firstCalls++
		return nil
	})
	runtime.Register(finalJobType, func(context.Context, Context) error {
		finalCalls++
		return nil
	})
	payload, err := json.Marshal(delivery)
	if err != nil {
		t.Fatalf("encode predecessor delivery: %v", err)
	}
	handler := queueRuntime.handlers[internalJobChainNode]
	if handler == nil {
		t.Fatal("chain delivery handler is not registered")
	}

	successorErr := errors.New("successor enqueue rejected")
	queueRuntime.dispatchErr = successorErr
	initialContext, initialSettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	initialContext = workflowGenerationContext(initialContext, owner)
	initialErr := handler(initialContext, testInboundJob{payload: payload})
	if !busruntime.IsUncommitted(initialErr) || !errors.Is(initialErr, successorErr) {
		t.Fatalf("initial predecessor error = %v, want uncommitted successor rejection", initialErr)
	}
	if firstCalls != 1 || finalCalls != 0 || !initialSettlement.ApplicationStateCommitted() {
		t.Fatalf("initial calls/application-state signal = %d/%d/%t, want 1/0/true", firstCalls, finalCalls, initialSettlement.ApplicationStateCommitted())
	}
	receiptStore := requireTransitionReceiptStore(t, store)
	receipt, known, err := receiptStore.chainTransitionReceipt(context.Background(), chainID, firstNodeID)
	if err != nil || !known || receipt.owner.deliveryID != owner {
		t.Fatalf("initial predecessor receipt = known:%t receipt:%+v err:%v", known, receipt, err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)

	for index, generationID := range []string{"generation-chain-recovery-one", "generation-chain-recovery-two"} {
		recoveryContext, recoverySettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
		recoveryContext = workflowRecoveryContext(recoveryContext, generationID, owner)
		recoveryErr := handler(recoveryContext, testInboundJob{payload: payload})
		if !busruntime.IsUncommitted(recoveryErr) || !errors.Is(recoveryErr, successorErr) {
			t.Fatalf("recovery %d error = %v, want uncommitted successor rejection", index+1, recoveryErr)
		}
		if recoverySettlement.ApplicationStateCommitted() {
			t.Fatalf("recovery %d marked current generation as transition owner", index+1)
		}
		receipt, known, err = receiptStore.chainTransitionReceipt(context.Background(), chainID, firstNodeID)
		if err != nil || !known || receipt.owner.deliveryID != owner {
			t.Fatalf("recovery %d predecessor receipt = known:%t receipt:%+v err:%v", index+1, known, receipt, err)
		}
	}
	if firstCalls != 1 || finalCalls != 0 {
		t.Fatalf("handler calls after repeated rejection = first:%d final:%d, want 1/0", firstCalls, finalCalls)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)

	queueRuntime.dispatchErr = nil
	recoveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	recoveryContext = workflowRecoveryContext(recoveryContext, "generation-chain-recovery-success", owner)
	if err := handler(recoveryContext, testInboundJob{payload: payload}); err != nil {
		t.Fatalf("recover successful predecessor: %v", err)
	}
	if firstCalls != 1 || finalCalls != 1 {
		t.Fatalf("handler calls after recovery = first:%d final:%d, want 1/1", firstCalls, finalCalls)
	}
	state, err := store.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get recovered chain: %v", err)
	}
	if !state.Completed || state.Failed || state.NextIndex != 2 {
		t.Fatalf("recovered chain state = %+v, want completed", state)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)
	settlement.Commit()

	var succeeded, advanced, completed int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventChainAdvanced:
			advanced++
		case EventChainCompleted:
			completed++
		}
	}
	if succeeded != 2 || advanced != 1 || completed != 1 {
		t.Fatalf("job/advance/completion counts = %d/%d/%d, want 2/1/1", succeeded, advanced, completed)
	}
}

// TestChainRecoveredPredecessorDoesNotRedispatchAfterSuccessorProgress proves
// recovery cannot duplicate a continuation after the immediate successor won.
func TestChainRecoveredPredecessorDoesNotRedispatchAfterSuccessorProgress(t *testing.T) {
	const (
		chainID      = "chain-recovered-progressed-successor"
		dispatchID   = "dispatch-recovered-progressed-successor"
		firstNodeID  = "node-recovered-progressed-first"
		secondNodeID = "node-recovered-progressed-second"
		finalNodeID  = "node-recovered-progressed-final"
		firstJobType = "workflow:chain:recovered-progressed-first"
		finalJobType = "workflow:chain:recovered-progressed-final"
		owner        = "generation-chain-progressed-predecessor"
	)
	store := NewMemoryStore()
	if err := store.CreateChain(context.Background(), ChainRecord{
		ChainID:    chainID,
		DispatchID: dispatchID,
		Nodes: []ChainNode{
			{NodeID: firstNodeID, Job: StoredJob{Type: firstJobType}},
			{NodeID: secondNodeID, Job: StoredJob{Type: "workflow:chain:recovered-progressed-second"}},
			{NodeID: finalNodeID, Job: StoredJob{Type: finalJobType}},
		},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        firstNodeID,
		JobID:         "job-recovered-progressed-first",
		Job:           StoredJob{Type: firstJobType},
	}
	if advanced, err := requireChainAdvanceStore(t, store).advanceChainOutcome(context.Background(), chainID, firstNodeID, workflowTransitionClaim(delivery, 2, owner)); err != nil || !advanced.claimedNow {
		t.Fatalf("advance predecessor = %+v, err:%v", advanced, err)
	}
	if next, done, err := store.AdvanceChain(context.Background(), chainID, secondNodeID); err != nil || done || next == nil || next.NodeID != finalNodeID {
		t.Fatalf("advance successor = next:%+v done:%t err:%v", next, done, err)
	}

	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var firstCalls, finalCalls int
	runtime.Register(firstJobType, func(context.Context, Context) error {
		firstCalls++
		return nil
	})
	runtime.Register(finalJobType, func(context.Context, Context) error {
		finalCalls++
		return nil
	})
	recoveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	recoveryContext = workflowRecoveryContext(recoveryContext, "generation-chain-progressed-recovery", owner)
	if err := queueRuntime.DispatchJSON(recoveryContext, internalJobChainNode, delivery); err != nil {
		t.Fatalf("recover progressed predecessor: %v", err)
	}
	if firstCalls != 0 || finalCalls != 0 {
		t.Fatalf("handler calls = first:%d final:%d, want 0/0", firstCalls, finalCalls)
	}
	state, err := store.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get progressed chain: %v", err)
	}
	if state.NextIndex != 2 || state.Completed || state.Failed {
		t.Fatalf("progressed chain state = %+v, want active final node", state)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)
	settlement.Commit()
	var succeeded, advanced, completed int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventChainAdvanced:
			advanced++
		case EventChainCompleted:
			completed++
		}
	}
	if succeeded != 1 || advanced != 1 || completed != 0 {
		t.Fatalf("recovered fact counts = job:%d advanced:%d completed:%d, want 1/1/0", succeeded, advanced, completed)
	}
}

// TestRecoverCommittedChainSuccessRejectsInconsistentState covers every
// validation boundary before recovery can publish a persisted winner fact.
func TestRecoverCommittedChainSuccessRejectsInconsistentState(t *testing.T) {
	baseNode := ChainNode{NodeID: "node-recovery-validation", Job: StoredJob{Type: "workflow:chain:recovery-validation"}}
	tests := []struct {
		name          string
		state         ChainState
		withoutProof  bool
		wantRecovered bool
		wantErr       bool
	}{
		{
			name:    "unknown node",
			state:   ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{{NodeID: "other-node"}}, NextIndex: 1, Completed: true},
			wantErr: true,
		},
		{
			name:    "negative next index",
			state:   ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{baseNode}, NextIndex: -1},
			wantErr: true,
		},
		{
			name:    "oversized next index",
			state:   ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{baseNode}, NextIndex: 2},
			wantErr: true,
		},
		{
			name: "all nodes advanced without completion",
			state: ChainState{
				ChainID:   "chain-recovery-validation",
				Nodes:     []ChainNode{baseNode, {NodeID: "node-recovery-final"}},
				NextIndex: 2,
			},
			wantErr: true,
		},
		{
			name: "completed before all nodes advanced",
			state: ChainState{
				ChainID:   "chain-recovery-validation",
				Nodes:     []ChainNode{baseNode, {NodeID: "node-recovery-final"}},
				NextIndex: 1,
				Completed: true,
			},
			wantErr: true,
		},
		{
			name:    "completed before node advance",
			state:   ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{baseNode}, Completed: true},
			wantErr: true,
		},
		{
			name:    "final node advanced without completion",
			state:   ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{baseNode}, NextIndex: 1},
			wantErr: true,
		},
		{
			name:  "current node remains unsettled",
			state: ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{baseNode}},
		},
		{
			name:    "chain identity mismatch",
			state:   ChainState{ChainID: "different-chain", Nodes: []ChainNode{baseNode}, NextIndex: 1, Completed: true},
			wantErr: true,
		},
		{
			name: "dispatch identity mismatch",
			state: ChainState{
				ChainID:    "chain-recovery-validation",
				DispatchID: "different-dispatch",
				Nodes:      []ChainNode{baseNode},
				NextIndex:  1,
				Completed:  true,
			},
			wantErr: true,
		},
		{
			name:    "persisted job type mismatch",
			state:   ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{{NodeID: baseNode.NodeID, Job: StoredJob{Type: "different-job"}}}, NextIndex: 1, Completed: true},
			wantErr: true,
		},
		{
			name: "persisted job payload mismatch",
			state: ChainState{
				ChainID:   "chain-recovery-validation",
				Nodes:     []ChainNode{{NodeID: baseNode.NodeID, Job: StoredJob{Type: baseNode.Job.Type, Payload: []byte(`{"different":true}`)}}},
				NextIndex: 1,
				Completed: true,
			},
			wantErr: true,
		},
		{
			name: "persisted job options mismatch",
			state: ChainState{
				ChainID:   "chain-recovery-validation",
				Nodes:     []ChainNode{{NodeID: baseNode.NodeID, Job: StoredJob{Type: baseNode.Job.Type, Options: JobOptions{Queue: "different"}}}},
				NextIndex: 1,
				Completed: true,
			},
			wantErr: true,
		},
		{
			name:         "missing recovery proof",
			state:        ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{baseNode}, NextIndex: 1, Completed: true},
			withoutProof: true,
		},
		{
			name:          "receipt capability absent",
			state:         ChainState{ChainID: "chain-recovery-validation", Nodes: []ChainNode{baseNode}, NextIndex: 1, Completed: true},
			wantRecovered: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			faultStore := &workflowMutationFaultStore{Store: NewMemoryStore(), getChainState: &test.state}
			runtime, _, recorder := newWorkflowMutationRuntime(t, faultStore)
			env := envelope{
				DispatchID: "dispatch-recovery-validation",
				ChainID:    "chain-recovery-validation",
				NodeID:     baseNode.NodeID,
				JobID:      "job-recovery-validation",
				Job:        baseNode.Job,
			}
			recoveryContext := context.Background()
			if !test.withoutProof {
				recoveryContext = workflowRecoveryContext(recoveryContext, "generation-chain-validation-current", "generation-chain-validation-recovered")
			}
			recovered, err := runtime.recoverCommittedChainSuccess(recoveryContext, env)
			wantRecovered := test.wantRecovered || test.wantErr
			if (err != nil) != test.wantErr || recovered != wantRecovered {
				t.Fatalf("recovery = recovered:%t err:%v, want recovered:%t err:%t", recovered, err, wantRecovered, test.wantErr)
			}
			if err != nil && !busruntime.IsUncommitted(err) {
				t.Fatalf("recovery validation error = %v, want uncommitted", err)
			}
			assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)
		})
	}
}

// TestBatchCommittedSuccessSurvivesContradictorySettlementReplay proves a
// terminal member's receipt remains observable after settlement failure
// without re-executing application code that could contradict it.
func TestBatchCommittedSuccessSurvivesContradictorySettlementReplay(t *testing.T) {
	const (
		batchID = "batch-committed-success-settlement-replay"
		jobID   = "job-committed-success-settlement-replay"
		jobType = "workflow:batch:committed-success-settlement-replay"
	)
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID:    batchID,
		DispatchID: "dispatch-committed-success-settlement-replay",
		Jobs:       []BatchJob{{JobID: jobID, Job: StoredJob{Type: jobType}}},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		if handlerCalls == 1 {
			return nil
		}
		return busruntime.Permanent(errors.New("contradictory replay failure"))
	})
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-committed-success-settlement-replay",
		Kind:          "batch_job",
		BatchID:       batchID,
		JobID:         jobID,
		Job:           StoredJob{Type: jobType},
	}

	firstContext, _ := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	firstContext = workflowGenerationContext(firstContext, "generation-batch-committed-success")
	if err := queueRuntime.DispatchJSON(firstContext, internalJobBatchJob, delivery); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventBatchProgressed, EventBatchCompleted)
	state, err := store.GetBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("get committed batch: %v", err)
	}
	if !state.Completed || state.Cancelled || state.Processed != 1 || state.Failed != 0 {
		t.Fatalf("committed batch state = %+v, want successful completion", state)
	}

	replayContext, replaySettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	replayContext = workflowRecoveryContext(replayContext, "generation-batch-replay", "generation-batch-committed-success")
	if err := queueRuntime.DispatchJSON(replayContext, internalJobBatchJob, delivery); err != nil {
		t.Fatalf("contradictory redelivery: %v", err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventBatchProgressed, EventBatchCompleted)
	replaySettlement.Commit()

	var succeeded, progressed, completed, failed int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			succeeded++
		case EventBatchProgressed:
			progressed++
		case EventBatchCompleted:
			completed++
		case EventJobFailed, EventBatchFailed, EventBatchCancelled:
			failed++
		}
	}
	if handlerCalls != 1 || succeeded != 1 || progressed != 1 || completed != 1 || failed != 0 {
		t.Fatalf("handler/job/progress/completion/failure counts = %d/%d/%d/%d/%d, want 1/1/1/1/0", handlerCalls, succeeded, progressed, completed, failed)
	}
}

// TestBatchCompletionReceiptIdentifiesCompletingMember proves member facts
// and aggregate completion are recovered only from their exact receipt owners.
func TestBatchCompletionReceiptIdentifiesCompletingMember(t *testing.T) {
	const (
		batchID       = "batch-recovery-terminal-owner"
		dispatchID    = "dispatch-batch-recovery-terminal-owner"
		staleJobID    = "job-batch-recovery-stale"
		terminalJobID = "job-batch-recovery-terminal"
		staleJobType  = "workflow:batch:recovery-stale"
		terminalType  = "workflow:batch:recovery-terminal"
	)
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID:    batchID,
		DispatchID: dispatchID,
		Jobs: []BatchJob{
			{JobID: staleJobID, Job: StoredJob{Type: staleJobType}},
			{JobID: terminalJobID, Job: StoredJob{Type: terminalType}},
		},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	stale := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "batch_job", BatchID: batchID, JobID: staleJobID, Job: StoredJob{Type: staleJobType}}
	terminal := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "batch_job", BatchID: batchID, JobID: terminalJobID, Job: StoredJob{Type: terminalType}}
	settlements := requireBatchSettlementStore(t, store)
	staleResult, err := settlements.settleBatchOutcome(context.Background(), batchID, staleJobID, BatchJobSucceeded, nil, workflowTransitionClaim(stale, 2, "generation-batch-stale"))
	if err != nil || !staleResult.claimedNow || staleResult.state.Completed || staleResult.receipt.aggregateCompleted {
		t.Fatalf("settle stale member = %+v, err:%v", staleResult, err)
	}
	terminalResult, err := settlements.settleBatchOutcome(context.Background(), batchID, terminalJobID, BatchJobSucceeded, nil, workflowTransitionClaim(terminal, 2, "generation-batch-terminal"))
	if err != nil || !terminalResult.claimedNow || !terminalResult.state.Completed || !terminalResult.receipt.aggregateCompleted {
		t.Fatalf("settle terminal member = %+v, err:%v", terminalResult, err)
	}

	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls int
	runtime.Register(staleJobType, func(context.Context, Context) error {
		handlerCalls++
		return busruntime.Permanent(errors.New("stale handler must not run"))
	})
	runtime.Register(terminalType, func(context.Context, Context) error {
		handlerCalls++
		return busruntime.Permanent(errors.New("terminal handler must not run"))
	})

	staleContext, staleSettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	staleContext = workflowRecoveryContext(staleContext, "generation-batch-stale-replay", "generation-batch-stale")
	if err := queueRuntime.DispatchJSON(staleContext, internalJobBatchJob, stale); err != nil {
		t.Fatalf("recover stale member: %v", err)
	}
	staleSettlement.Commit()
	assertNoCommittedEvents(t, recorder.events, EventBatchCompleted)

	terminalContext, terminalSettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	terminalContext = workflowRecoveryContext(terminalContext, "generation-batch-terminal-replay", "generation-batch-terminal")
	if err := queueRuntime.DispatchJSON(terminalContext, internalJobBatchJob, terminal); err != nil {
		t.Fatalf("recover terminal member: %v", err)
	}
	terminalSettlement.Commit()

	var staleSucceeded, terminalSucceeded, progressed, completed int
	completedJobID := ""
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobSucceeded:
			if event.JobID == staleJobID {
				staleSucceeded++
			}
			if event.JobID == terminalJobID {
				terminalSucceeded++
			}
		case EventBatchProgressed:
			progressed++
		case EventBatchCompleted:
			completed++
			completedJobID = event.JobID
		}
	}
	if handlerCalls != 0 || staleSucceeded != 1 || terminalSucceeded != 1 || progressed != 2 || completed != 1 || completedJobID != terminalJobID {
		t.Fatalf("handlers/stale/terminal/progress/completion/completer = %d/%d/%d/%d/%d/%q, want 0/1/1/2/1/%q", handlerCalls, staleSucceeded, terminalSucceeded, progressed, completed, completedJobID, terminalJobID)
	}
}

// TestBatchCompletionReceiptSurvivesFailedCompletingMember proves an
// allow-failures aggregate can recover completion even though the member error
// itself is intentionally absent from the durable receipt.
func TestBatchCompletionReceiptSurvivesFailedCompletingMember(t *testing.T) {
	const (
		batchID    = "batch-recovery-failed-completer"
		dispatchID = "dispatch-batch-recovery-failed-completer"
		firstJobID = "job-batch-recovery-first"
		finalJobID = "job-batch-recovery-failed-completer"
		finalType  = "workflow:batch:recovery-failed-completer"
	)
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID:     batchID,
		DispatchID:  dispatchID,
		AllowFailed: true,
		Jobs: []BatchJob{
			{JobID: firstJobID, Job: StoredJob{Type: "workflow:batch:recovery-first"}},
			{JobID: finalJobID, Job: StoredJob{Type: finalType}},
		},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	first := envelope{DispatchID: dispatchID, BatchID: batchID, JobID: firstJobID, Job: StoredJob{Type: "workflow:batch:recovery-first"}}
	final := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "batch_job", BatchID: batchID, JobID: finalJobID, Job: StoredJob{Type: finalType}}
	settlements := requireBatchSettlementStore(t, store)
	if result, err := settlements.settleBatchOutcome(context.Background(), batchID, firstJobID, BatchJobSucceeded, nil, workflowTransitionClaim(first, 2, "generation-batch-first")); err != nil || !result.claimedNow || result.state.Completed {
		t.Fatalf("settle first member = %+v, err:%v", result, err)
	}
	originalCause := errors.New("durable cause remains delivery-local")
	result, err := settlements.settleBatchOutcome(context.Background(), batchID, finalJobID, BatchJobFailed, originalCause, workflowTransitionClaim(final, 2, "generation-batch-failed-completer"))
	if err != nil || !result.claimedNow || !result.state.Completed || result.state.Cancelled || !result.receipt.aggregateCompleted {
		t.Fatalf("settle failed completer = %+v, err:%v", result, err)
	}

	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls int
	runtime.Register(finalType, func(context.Context, Context) error {
		handlerCalls++
		return errors.New("handler must not run")
	})
	recoveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	recoveryContext = workflowRecoveryContext(recoveryContext, "generation-batch-failed-completer-replay", "generation-batch-failed-completer")
	recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobBatchJob, final)
	if !busruntime.IsPermanent(recoveryErr) || busruntime.IsUncommitted(recoveryErr) || errors.Is(recoveryErr, originalCause) {
		t.Fatalf("recover failed completer error = %v, want generic permanent settlement without original cause", recoveryErr)
	}
	assertNoCommittedEvents(t, recorder.events, EventBatchCompleted)
	settlement.Commit()
	nestedContext, nestedSettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	nestedContext = workflowRecoveryContext(nestedContext, "generation-batch-failed-completer-replay-2", "generation-batch-failed-completer-replay")
	nestedErr := queueRuntime.DispatchJSON(nestedContext, internalJobBatchJob, final)
	if !busruntime.IsPermanent(nestedErr) || busruntime.IsUncommitted(nestedErr) || errors.Is(nestedErr, originalCause) {
		t.Fatalf("recover failed completer after another unsettled generation = %v, want generic permanent settlement", nestedErr)
	}
	nestedSettlement.Commit()

	var completed, memberFacts int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventBatchCompleted:
			completed++
			if event.JobID != finalJobID {
				t.Fatalf("completion job id = %q, want %q", event.JobID, finalJobID)
			}
		case EventJobSucceeded, EventJobFailed, EventBatchProgressed:
			memberFacts++
		}
	}
	if handlerCalls != 0 || completed != 1 || memberFacts != 0 {
		t.Fatalf("handler/completion/member facts = %d/%d/%d, want 0/1/0", handlerCalls, completed, memberFacts)
	}
}

// TestBatchRecoveryRejectsInvalidAggregateReceiptShape proves corrupt terminal
// ownership cannot acknowledge a recovered member or publish partial facts.
func TestBatchRecoveryRejectsInvalidAggregateReceiptShape(t *testing.T) {
	tests := []struct {
		name       string
		diagnostic string
		mutate     func(*memoryStore, transitionReceiptKey)
	}{
		{name: "completion for nonterminal state", diagnostic: "nonterminal state", mutate: func(store *memoryStore, _ transitionReceiptKey) {
			state := &store.batch["batch-invalid-aggregate-receipt"].state
			state.Total = 2
			state.Pending = 1
			state.Processed = 1
			state.Completed = false
		}},
		{name: "cancellation without completion", diagnostic: "cancellation is not completed", mutate: func(store *memoryStore, key transitionReceiptKey) {
			receipt := store.transitionReceipts[key]
			receipt.aggregateCompleted = false
			receipt.aggregateCancelled = true
			store.transitionReceipts[key] = receipt
		}},
		{name: "cancellation owns success", diagnostic: "does not own failure", mutate: func(store *memoryStore, key transitionReceiptKey) {
			receipt := store.transitionReceipts[key]
			receipt.aggregateCancelled = true
			store.transitionReceipts[key] = receipt
			store.batch["batch-invalid-aggregate-receipt"].state.Cancelled = true
		}},
		{name: "cancellation disagrees with state", diagnostic: "does not match aggregate state", mutate: func(store *memoryStore, key transitionReceiptKey) {
			receipt := store.transitionReceipts[key]
			receipt.outcome = BatchJobFailed
			receipt.aggregateCancelled = true
			store.transitionReceipts[key] = receipt
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				batchID    = "batch-invalid-aggregate-receipt"
				dispatchID = "dispatch-invalid-aggregate-receipt"
				jobID      = "job-invalid-aggregate-receipt"
				jobType    = "workflow:batch:invalid-aggregate-receipt"
				owner      = "generation-invalid-aggregate-receipt"
			)
			store := NewMemoryStore().(*memoryStore)
			env := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "batch_job", BatchID: batchID, JobID: jobID, Job: StoredJob{Type: jobType}}
			if err := store.CreateBatch(context.Background(), BatchRecord{BatchID: batchID, DispatchID: dispatchID, Jobs: []BatchJob{{JobID: jobID, Job: env.Job}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			if settled, err := store.settleBatchOutcome(context.Background(), batchID, jobID, BatchJobSucceeded, nil, workflowTransitionClaim(env, 2, owner)); err != nil || !settled.receiptKnown || !settled.receipt.aggregateCompleted {
				t.Fatalf("settle batch = %+v err:%v", settled, err)
			}
			key := transitionReceiptKey{workflowKind: batchTransitionKind, workflowID: batchID, memberID: jobID}
			store.mu.Lock()
			test.mutate(store, key)
			store.mu.Unlock()

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, callbackCalls int
			runtime.Register(jobType, func(context.Context, Context) error { handlerCalls++; return nil })
			runtime.batchCallbacks[batchID] = batchCallbacks{finally: func(context.Context, BatchState) error { callbackCalls++; return nil }}
			recoveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
			recoveryContext = workflowRecoveryContext(recoveryContext, "generation-invalid-aggregate-current", owner)
			recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobBatchJob, env)
			if !busruntime.IsUncommitted(recoveryErr) || !strings.Contains(recoveryErr.Error(), test.diagnostic) {
				t.Fatalf("invalid aggregate receipt recovery = %v, want uncommitted %q", recoveryErr, test.diagnostic)
			}
			if handlerCalls != 0 || callbackCalls != 0 || settlement.ApplicationStateCommitted() || len(recorder.events) != 0 {
				t.Fatalf("handler/callback/committed/events = %d/%d/%t/%d, want 0/0/false/0", handlerCalls, callbackCalls, settlement.ApplicationStateCommitted(), len(recorder.events))
			}
		})
	}
}

// TestRecoverCommittedBatchTransitionRejectsInconsistentState covers every
// aggregate validation branch before recovery can publish member facts.
func TestRecoverCommittedBatchTransitionRejectsInconsistentState(t *testing.T) {
	env := envelope{
		DispatchID: "dispatch-batch-recovery-validation",
		BatchID:    "batch-recovery-validation",
		JobID:      "job-batch-recovery-validation",
		Job:        StoredJob{Type: "workflow:batch:recovery-validation"},
	}
	valid := BatchState{
		BatchID:    env.BatchID,
		DispatchID: env.DispatchID,
		Total:      2,
		Pending:    1,
		Processed:  1,
	}
	tests := []struct {
		name         string
		state        BatchState
		withoutProof bool
		valid        bool
	}{
		{name: "batch identity mismatch", state: func() BatchState { state := valid; state.BatchID = "different-batch"; return state }()},
		{name: "dispatch identity mismatch", state: func() BatchState { state := valid; state.DispatchID = "different-dispatch"; return state }()},
		{name: "nonpositive total", state: func() BatchState { state := valid; state.Total = 0; return state }()},
		{name: "negative pending", state: func() BatchState { state := valid; state.Pending = -1; return state }()},
		{name: "negative processed", state: func() BatchState { state := valid; state.Processed = -1; return state }()},
		{name: "negative failed", state: func() BatchState { state := valid; state.Failed = -1; return state }()},
		{name: "counter sum mismatch", state: func() BatchState { state := valid; state.Total = 3; return state }()},
		{name: "failures exceed processed", state: func() BatchState { state := valid; state.Failed = 2; return state }()},
		{name: "exhausted without completion", state: BatchState{BatchID: env.BatchID, DispatchID: env.DispatchID, Total: 1, Processed: 1}},
		{name: "completed with pending member", state: func() BatchState { state := valid; state.Completed = true; return state }()},
		{name: "missing recovery proof", state: valid, withoutProof: true, valid: true},
		{name: "valid aggregate without receipt", state: valid, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			faultStore := &workflowMutationFaultStore{Store: NewMemoryStore(), getBatchState: &test.state}
			runtime, _, recorder := newWorkflowMutationRuntime(t, faultStore)
			recoveryContext := context.Background()
			if !test.withoutProof {
				recoveryContext = workflowRecoveryContext(recoveryContext, "generation-batch-validation-current", "generation-batch-validation-recovered")
			}
			handled, err := runtime.recoverCommittedBatchTransition(recoveryContext, env)
			wantErr := !test.withoutProof && !test.valid
			if (err != nil) != wantErr || handled != wantErr {
				t.Fatalf("recovery = handled:%t err:%v, want handled:%t err:%t", handled, err, wantErr, wantErr)
			}
			if err != nil && !busruntime.IsUncommitted(err) {
				t.Fatalf("recovery validation error = %v, want uncommitted", err)
			}
			assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventBatchProgressed, EventBatchCompleted)
		})
	}
}

// TestChainFailureReadFailureRedeliversWithoutFacts proves a committed failure
// is replayed until compatibility stores can expose its authoritative state.
func TestChainFailureReadFailureRedeliversWithoutFacts(t *testing.T) {
	committedCause := errors.New("first application failure")
	replayedCause := errors.New("different replayed failure")
	committedErr := busruntime.Permanent(committedCause)
	replayedErr := busruntime.Permanent(replayedCause)
	const (
		chainID = "chain-failure-read-failure"
		nodeID  = "node-failure-read-failure"
		jobType = "workflow:chain:failure-read-failure"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes:   []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	faultStore := &workflowMutationFaultStore{Store: baseStore, getChainErrOnCall: 2}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)
	var handlerCalls, catchCalls, finallyCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		if handlerCalls == 1 {
			return committedErr
		}
		return replayedErr
	})
	var observedCatchErr error
	runtime.chainCallbacks[chainID] = chainCallbacks{
		catch: func(_ context.Context, _ ChainState, err error) error {
			catchCalls++
			observedCatchErr = err
			return nil
		},
		finally: func(context.Context, ChainState) error {
			finallyCalls++
			return nil
		},
	}
	delivery := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-failure-read-failure",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-failure-read-failure",
		Job:           StoredJob{Type: jobType},
	}
	deliveryContext := busruntime.WithDeliveryAttempt(context.Background(), busruntime.DeliveryAttempt{Number: 0, MaxRetry: 2})
	err := queueRuntime.DispatchJSON(deliveryContext, internalJobChainNode, delivery)
	if !busruntime.IsUncommitted(err) || !strings.Contains(err.Error(), "injected chain read failure") {
		t.Fatalf("failure confirmation error = %v, want uncommitted injected read failure", err)
	}
	if handlerCalls != 1 || catchCalls != 0 || finallyCalls != 0 {
		t.Fatalf("handler/catch/finally calls before recovery = %d/%d/%d, want 1/0/0", handlerCalls, catchCalls, finallyCalls)
	}
	state, err := baseStore.GetChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("get committed chain: %v", err)
	}
	if state.Completed || !state.Failed || state.Failure != committedErr.Error() {
		t.Fatalf("committed chain state = %+v, want failed only", state)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobFailed, EventChainFailed, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed)

	if err := queueRuntime.DispatchJSON(deliveryContext, internalJobChainNode, delivery); !errors.Is(err, replayedCause) || !busruntime.IsPermanent(err) {
		t.Fatalf("redeliver after store recovery: %v", err)
	}
	if handlerCalls != 2 || catchCalls != 1 || finallyCalls != 1 {
		t.Fatalf("handler/catch/finally calls after recovery = %d/%d/%d, want 2/1/1", handlerCalls, catchCalls, finallyCalls)
	}
	if observedCatchErr == nil || observedCatchErr.Error() != committedErr.Error() {
		t.Fatalf("catch error = %v, want committed cause %v", observedCatchErr, committedErr)
	}
	if errors.Is(observedCatchErr, replayedCause) {
		t.Fatalf("catch error retained replayed cause: %v", observedCatchErr)
	}
	var failed, chainFailed, callbackSucceeded int
	for _, event := range recorder.events {
		switch event.Kind {
		case EventJobFailed:
			failed++
			if event.Err == nil || event.Err.Error() != committedErr.Error() || !busruntime.IsPermanent(event.Err) || errors.Is(event.Err, replayedCause) {
				t.Fatalf("job failure cause = %v, want %v", event.Err, committedErr)
			}
		case EventChainFailed:
			chainFailed++
			if event.Err == nil || event.Err.Error() != committedErr.Error() || !busruntime.IsPermanent(event.Err) || errors.Is(event.Err, replayedCause) {
				t.Fatalf("chain failure cause = %v, want %v", event.Err, committedErr)
			}
		case EventCallbackSucceeded:
			callbackSucceeded++
		}
	}
	if failed != 1 || chainFailed != 1 || callbackSucceeded != 2 {
		t.Fatalf("job/chain/callback failure events = %d/%d/%d, want 1/1/2", failed, chainFailed, callbackSucceeded)
	}
}

// TestChainDoneRequiresTerminalState rejects custom stores that report a
// terminal transition while their readable state remains active.
func TestChainDoneRequiresTerminalState(t *testing.T) {
	const (
		chainID = "chain-inconsistent-done"
		nodeID  = "node-inconsistent-done"
		jobType = "workflow:chain:inconsistent-done"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes:   []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	faultStore := &workflowMutationFaultStore{Store: baseStore, advanceDoneWithoutState: true}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)
	runtime.Register(jobType, func(context.Context, Context) error { return nil })
	err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-inconsistent-done",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-inconsistent-done",
		Job:           StoredJob{Type: jobType},
	})
	if !busruntime.IsUncommitted(err) || !strings.Contains(err.Error(), "done without terminal state") {
		t.Fatalf("inconsistent store error = %v, want uncommitted terminal-state validation", err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobSucceeded, EventChainAdvanced, EventChainCompleted)
	state, stateErr := baseStore.GetChain(context.Background(), chainID)
	if stateErr != nil {
		t.Fatalf("get active chain: %v", stateErr)
	}
	if state.NextIndex != 0 || state.Completed || state.Failed {
		t.Fatalf("inconsistent store changed chain state: %+v", state)
	}
}

// TestChainFailureRequiresTerminalState rejects compatibility stores that
// acknowledge failure while leaving the chain active and readable.
func TestChainFailureRequiresTerminalState(t *testing.T) {
	const (
		chainID = "chain-inconsistent-failure"
		nodeID  = "node-inconsistent-failure"
		jobType = "workflow:chain:inconsistent-failure"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes:   []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	faultStore := &workflowMutationFaultStore{Store: baseStore, failChainWithoutState: true}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, faultStore)
	runtime.Register(jobType, func(context.Context, Context) error { return errors.New("application failed") })
	err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-inconsistent-failure",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-inconsistent-failure",
		Job:           StoredJob{Type: jobType},
	})
	if !busruntime.IsUncommitted(err) || !strings.Contains(err.Error(), "accepted failure without terminal state") {
		t.Fatalf("inconsistent store error = %v, want uncommitted terminal-state validation", err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobFailed, EventChainFailed, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed)
	state, stateErr := baseStore.GetChain(context.Background(), chainID)
	if stateErr != nil {
		t.Fatalf("get active chain: %v", stateErr)
	}
	if state.NextIndex != 0 || state.Completed || state.Failed {
		t.Fatalf("inconsistent store changed chain state: %+v", state)
	}
}

// TestChainFailureFallbackCommitsAndConfirmsState covers established custom
// stores that have not added the first-writer outcome capability.
func TestChainFailureFallbackCommitsAndConfirmsState(t *testing.T) {
	const (
		chainID = "chain-compatibility-failure-fallback"
		nodeID  = "node-compatibility-failure-fallback"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes:   []ChainNode{{NodeID: nodeID}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	compatibilityStore := &workflowMutationFaultStore{Store: baseStore}
	runtime, _, _ := newWorkflowMutationRuntime(t, compatibilityStore)
	cause := errors.New("compatibility failure")
	state, owned, err := runtime.failChainNode(context.Background(), chainID, nodeID, cause)
	if err != nil || !owned || !state.Failed || state.Completed || state.Failure != cause.Error() {
		t.Fatalf("fallback failure = state:%+v owned:%t err:%v", state, owned, err)
	}
	state, owned, err = runtime.failChainNode(context.Background(), chainID, nodeID, errors.New("replacement failure"))
	if err != nil || !owned || state.Failure != cause.Error() {
		t.Fatalf("fallback replay = state:%+v owned:%t err:%v", state, owned, err)
	}
}

// TestAtomicChainFailureRequiresTerminalState rejects a capable custom store
// that claims ownership without exposing the committed terminal transition.
func TestAtomicChainFailureRequiresTerminalState(t *testing.T) {
	const (
		chainID = "chain-inconsistent-atomic-failure"
		nodeID  = "node-inconsistent-atomic-failure"
		jobType = "workflow:chain:inconsistent-atomic-failure"
	)
	baseStore := NewMemoryStore()
	if err := baseStore.CreateChain(context.Background(), ChainRecord{
		ChainID: chainID,
		Nodes:   []ChainNode{{NodeID: nodeID, Job: StoredJob{Type: jobType}}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, nonterminalWorkflowOutcomeStore{Store: baseStore})
	runtime.Register(jobType, func(context.Context, Context) error { return errors.New("application failed") })
	err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch-inconsistent-atomic-failure",
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        nodeID,
		JobID:         "job-inconsistent-atomic-failure",
		Job:           StoredJob{Type: jobType},
	})
	if !busruntime.IsUncommitted(err) || !strings.Contains(err.Error(), "accepted failure without terminal state") {
		t.Fatalf("inconsistent atomic store error = %v, want uncommitted terminal-state validation", err)
	}
	assertNoCommittedEvents(t, recorder.events, EventJobFailed, EventChainFailed, EventCallbackStarted, EventCallbackSucceeded, EventCallbackFailed)
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
