package queue

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

type workflowStoreAdapterContextKey struct{}

type workflowStoreAdapterSpy struct {
	wantContext context.Context
	contextsOK  bool

	createChainRecord ChainRecord
	createChainErr    error

	advanceChainID       string
	advanceCompletedNode string
	advanceNext          *ChainNode
	advanceDone          bool
	advanceErr           error

	failChainID    string
	failChainCause error
	failChainErr   error

	getChainID    string
	getChainState ChainState
	getChainErr   error

	createBatchRecord BatchRecord
	createBatchErr    error

	startedBatchID string
	startedJobID   string
	startedErr     error

	succeededBatchID string
	succeededJobID   string
	succeededState   BatchState
	succeededDone    bool
	succeededErr     error

	failedBatchID string
	failedJobID   string
	failedCause   error
	failedState   BatchState
	failedDone    bool
	failedErr     error

	cancelBatchID string
	cancelErr     error

	getBatchID    string
	getBatchState BatchState
	getBatchErr   error

	callbackKey     string
	callbackClaimed bool
	callbackErr     error

	pruneBefore time.Time
	pruneErr    error
}

type workflowOutcomeStoreSpy struct {
	*workflowStoreAdapterSpy
	failNodeChainID string
	failNodeID      string
	failNodeCause   error
	failNodeState   ChainState
	failNodeOwned   bool
	failNodeErr     error
	settleBatchID   string
	settleJobID     string
	settleOutcome   BatchJobOutcome
	settleCause     error
	settleState     BatchState
	settleOwned     bool
	settleErr       error
}

// FailChainNode records the additive atomic chain transition.
func (s *workflowOutcomeStoreSpy) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error) {
	s.recordContext(ctx)
	s.failNodeChainID = chainID
	s.failNodeID = nodeID
	s.failNodeCause = cause
	return s.failNodeState, s.failNodeOwned, s.failNodeErr
}

// SettleBatchJob records the additive atomic member transition.
func (s *workflowOutcomeStoreSpy) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error) (BatchState, bool, error) {
	s.recordContext(ctx)
	s.settleBatchID = batchID
	s.settleJobID = jobID
	s.settleOutcome = outcome
	s.settleCause = cause
	return s.settleState, s.settleOwned, s.settleErr
}

// recordContext verifies that adapters forward the caller's context without replacement.
func (s *workflowStoreAdapterSpy) recordContext(ctx context.Context) {
	s.contextsOK = s.contextsOK && ctx == s.wantContext
}

// CreateChain records the converted root chain creation model.
func (s *workflowStoreAdapterSpy) CreateChain(ctx context.Context, record ChainRecord) error {
	s.recordContext(ctx)
	s.createChainRecord = record
	return s.createChainErr
}

// AdvanceChain records transition arguments and returns the configured physical successor.
func (s *workflowStoreAdapterSpy) AdvanceChain(ctx context.Context, chainID string, completedNode string) (*ChainNode, bool, error) {
	s.recordContext(ctx)
	s.advanceChainID = chainID
	s.advanceCompletedNode = completedNode
	return s.advanceNext, s.advanceDone, s.advanceErr
}

// FailChain records the terminal chain failure without wrapping its cause.
func (s *workflowStoreAdapterSpy) FailChain(ctx context.Context, chainID string, cause error) error {
	s.recordContext(ctx)
	s.failChainID = chainID
	s.failChainCause = cause
	return s.failChainErr
}

// GetChain records the lookup and returns configured physical chain state.
func (s *workflowStoreAdapterSpy) GetChain(ctx context.Context, chainID string) (ChainState, error) {
	s.recordContext(ctx)
	s.getChainID = chainID
	return s.getChainState, s.getChainErr
}

// CreateBatch records the converted root batch creation model.
func (s *workflowStoreAdapterSpy) CreateBatch(ctx context.Context, record BatchRecord) error {
	s.recordContext(ctx)
	s.createBatchRecord = record
	return s.createBatchErr
}

// MarkBatchJobStarted records the member start transition.
func (s *workflowStoreAdapterSpy) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	s.recordContext(ctx)
	s.startedBatchID = batchID
	s.startedJobID = jobID
	return s.startedErr
}

// MarkBatchJobSucceeded records the member success transition and returns configured aggregate state.
func (s *workflowStoreAdapterSpy) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error) {
	s.recordContext(ctx)
	s.succeededBatchID = batchID
	s.succeededJobID = jobID
	return s.succeededState, s.succeededDone, s.succeededErr
}

// MarkBatchJobFailed records the member failure transition and returns configured aggregate state.
func (s *workflowStoreAdapterSpy) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (BatchState, bool, error) {
	s.recordContext(ctx)
	s.failedBatchID = batchID
	s.failedJobID = jobID
	s.failedCause = cause
	return s.failedState, s.failedDone, s.failedErr
}

// CancelBatch records aggregate cancellation.
func (s *workflowStoreAdapterSpy) CancelBatch(ctx context.Context, batchID string) error {
	s.recordContext(ctx)
	s.cancelBatchID = batchID
	return s.cancelErr
}

// GetBatch records the lookup and returns configured physical aggregate state.
func (s *workflowStoreAdapterSpy) GetBatch(ctx context.Context, batchID string) (BatchState, error) {
	s.recordContext(ctx)
	s.getBatchID = batchID
	return s.getBatchState, s.getBatchErr
}

// MarkCallbackInvoked records the idempotency claim and returns its configured outcome.
func (s *workflowStoreAdapterSpy) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	s.recordContext(ctx)
	s.callbackKey = key
	return s.callbackClaimed, s.callbackErr
}

// Prune records the exact retention boundary.
func (s *workflowStoreAdapterSpy) Prune(ctx context.Context, before time.Time) error {
	s.recordContext(ctx)
	s.pruneBefore = before
	return s.pruneErr
}

// TestRootWorkflowStoreAdapterConvertsEveryMethod pins the complete custom-store boundary in both directions.
func TestRootWorkflowStoreAdapterConvertsEveryMethod(t *testing.T) {
	ctx := context.WithValue(context.Background(), workflowStoreAdapterContextKey{}, "adapter-test")
	createdAt := time.Date(2026, time.July, 18, 10, 11, 12, 13, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	createChainErr := errors.New("create chain failed")
	advanceErr := errors.New("advance chain failed")
	failCause := errors.New("chain handler failed")
	failChainErr := errors.New("fail chain failed")
	getChainErr := errors.New("get chain failed")
	createBatchErr := errors.New("create batch failed")
	startedErr := errors.New("start batch job failed")
	succeededErr := errors.New("succeed batch job failed")
	memberCause := errors.New("batch member failed")
	failedErr := errors.New("fail batch job failed")
	cancelErr := errors.New("cancel batch failed")
	getBatchErr := errors.New("get batch failed")
	callbackErr := errors.New("callback claim failed")
	pruneErr := errors.New("prune failed")

	chainState := ChainState{
		ChainID:    "chain-state",
		DispatchID: "dispatch-state",
		Queue:      "critical",
		Nodes: []ChainNode{{
			NodeID: "node-state",
			Job: StoredJob{
				Type:    "reports:state",
				Payload: []byte(`{"state":true}`),
				Options: StoredJobOptions{Queue: "critical", Delay: time.Second, Timeout: 2 * time.Second, Retry: 3, Backoff: 4 * time.Second, UniqueFor: 5 * time.Second},
			},
		}},
		NextIndex: 1,
		Completed: false,
		Failed:    true,
		Failure:   "state failure",
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
	batchState := BatchState{
		BatchID:     "batch-state",
		DispatchID:  "dispatch-batch-state",
		Name:        "nightly",
		Queue:       "bulk",
		AllowFailed: true,
		Total:       5,
		Pending:     2,
		Processed:   3,
		Failed:      1,
		Cancelled:   true,
		Completed:   false,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
	nextNode := &ChainNode{
		NodeID: "node-next",
		Job: StoredJob{
			Type:    "reports:next",
			Payload: []byte(`{"next":true}`),
			Options: StoredJobOptions{Queue: "critical", Retry: 7},
		},
	}
	spy := &workflowStoreAdapterSpy{
		wantContext:     ctx,
		contextsOK:      true,
		createChainErr:  createChainErr,
		advanceNext:     nextNode,
		advanceDone:     true,
		advanceErr:      advanceErr,
		failChainErr:    failChainErr,
		getChainState:   chainState,
		getChainErr:     getChainErr,
		createBatchErr:  createBatchErr,
		startedErr:      startedErr,
		succeededState:  batchState,
		succeededDone:   true,
		succeededErr:    succeededErr,
		failedState:     batchState,
		failedDone:      false,
		failedErr:       failedErr,
		cancelErr:       cancelErr,
		getBatchState:   batchState,
		getBatchErr:     getBatchErr,
		callbackClaimed: true,
		callbackErr:     callbackErr,
		pruneErr:        pruneErr,
	}
	adapter := rootWorkflowStoreAdapter{store: spy}

	engineChainRecord := workflow.ChainRecord{
		ChainID:    "chain-create",
		DispatchID: "dispatch-create",
		Queue:      "critical",
		Nodes: []workflow.ChainNode{{
			NodeID: "node-create",
			Job: workflow.StoredJob{
				Type:    "reports:create",
				Payload: []byte(`{"create":true}`),
				Options: workflow.JobOptions{Queue: "critical", Delay: time.Second, Timeout: 2 * time.Second, Retry: 3, Backoff: 4 * time.Second, UniqueFor: 5 * time.Second},
			},
		}},
		CreatedAt: createdAt,
	}
	wantRootChainRecord := ChainRecord{
		ChainID:    "chain-create",
		DispatchID: "dispatch-create",
		Queue:      "critical",
		Nodes: []ChainNode{{
			NodeID: "node-create",
			Job: StoredJob{
				Type:    "reports:create",
				Payload: []byte(`{"create":true}`),
				Options: StoredJobOptions{Queue: "critical", Delay: time.Second, Timeout: 2 * time.Second, Retry: 3, Backoff: 4 * time.Second, UniqueFor: 5 * time.Second},
			},
		}},
		CreatedAt: createdAt,
	}
	if err := adapter.CreateChain(ctx, engineChainRecord); err != createChainErr {
		t.Fatalf("CreateChain error = %v, want exact sentinel", err)
	}
	if !reflect.DeepEqual(spy.createChainRecord, wantRootChainRecord) {
		t.Fatalf("CreateChain record = %+v, want %+v", spy.createChainRecord, wantRootChainRecord)
	}
	engineChainRecord.Nodes[0].Job.Payload[0] = '!'
	if got := string(spy.createChainRecord.Nodes[0].Job.Payload); got != `{"create":true}` {
		t.Fatalf("CreateChain payload aliased engine bytes: %q", got)
	}

	next, done, err := adapter.AdvanceChain(ctx, "chain-advance", "node-complete")
	if err != advanceErr || !done || spy.advanceChainID != "chain-advance" || spy.advanceCompletedNode != "node-complete" {
		t.Fatalf("AdvanceChain result = next:%+v done:%t err:%v args:%q/%q", next, done, err, spy.advanceChainID, spy.advanceCompletedNode)
	}
	wantEngineNext := workflow.ChainNode{
		NodeID: "node-next",
		Job: workflow.StoredJob{
			Type:    "reports:next",
			Payload: []byte(`{"next":true}`),
			Options: workflow.JobOptions{Queue: "critical", Retry: 7},
		},
	}
	if next == nil || !reflect.DeepEqual(*next, wantEngineNext) {
		t.Fatalf("AdvanceChain next = %+v, want %+v", next, wantEngineNext)
	}
	next.Job.Payload[0] = '?'
	if got := string(spy.advanceNext.Job.Payload); got != `{"next":true}` {
		t.Fatalf("AdvanceChain output payload aliased physical bytes: %q", got)
	}
	spy.advanceNext = nil
	spy.advanceDone = false
	nilNext, nilDone, nilErr := adapter.AdvanceChain(ctx, "chain-nil", "node-nil")
	if nilNext != nil || nilDone || nilErr != advanceErr {
		t.Fatalf("AdvanceChain nil successor = next:%+v done:%t err:%v", nilNext, nilDone, nilErr)
	}

	if err := adapter.FailChain(ctx, "chain-fail", failCause); err != failChainErr || spy.failChainID != "chain-fail" || spy.failChainCause != failCause {
		t.Fatalf("FailChain result = err:%v id:%q cause:%v", err, spy.failChainID, spy.failChainCause)
	}
	gotChainState, err := adapter.GetChain(ctx, "chain-get")
	if err != getChainErr || spy.getChainID != "chain-get" {
		t.Fatalf("GetChain result = err:%v id:%q", err, spy.getChainID)
	}
	if gotChainState.ChainID != chainState.ChainID || gotChainState.DispatchID != chainState.DispatchID || gotChainState.Queue != chainState.Queue || gotChainState.NextIndex != chainState.NextIndex || gotChainState.Completed != chainState.Completed || gotChainState.Failed != chainState.Failed || gotChainState.Failure != chainState.Failure || !gotChainState.CreatedAt.Equal(chainState.CreatedAt) || !gotChainState.UpdatedAt.Equal(chainState.UpdatedAt) || len(gotChainState.Nodes) != 1 || gotChainState.Nodes[0].Job.Options.Retry != 3 {
		t.Fatalf("GetChain converted state = %+v, want fields from %+v", gotChainState, chainState)
	}
	gotChainState.Nodes[0].Job.Payload[0] = '#'
	if got := string(spy.getChainState.Nodes[0].Job.Payload); got != `{"state":true}` {
		t.Fatalf("GetChain payload aliased physical bytes: %q", got)
	}

	engineBatchRecord := workflow.BatchRecord{
		BatchID:     "batch-create",
		DispatchID:  "dispatch-batch-create",
		Name:        "daily",
		Queue:       "bulk",
		AllowFailed: true,
		Jobs: []workflow.BatchJob{{
			JobID: "job-create",
			Job: workflow.StoredJob{
				Type:    "reports:batch",
				Payload: []byte(`{"batch":true}`),
				Options: workflow.JobOptions{Queue: "bulk", Delay: 6 * time.Second, Timeout: 7 * time.Second, Retry: 8, Backoff: 9 * time.Second, UniqueFor: 10 * time.Second},
			},
		}},
		CreatedAt: createdAt,
	}
	wantRootBatchRecord := BatchRecord{
		BatchID:     "batch-create",
		DispatchID:  "dispatch-batch-create",
		Name:        "daily",
		Queue:       "bulk",
		AllowFailed: true,
		Jobs: []BatchJob{{
			JobID: "job-create",
			Job: StoredJob{
				Type:    "reports:batch",
				Payload: []byte(`{"batch":true}`),
				Options: StoredJobOptions{Queue: "bulk", Delay: 6 * time.Second, Timeout: 7 * time.Second, Retry: 8, Backoff: 9 * time.Second, UniqueFor: 10 * time.Second},
			},
		}},
		CreatedAt: createdAt,
	}
	if err := adapter.CreateBatch(ctx, engineBatchRecord); err != createBatchErr {
		t.Fatalf("CreateBatch error = %v, want exact sentinel", err)
	}
	if !reflect.DeepEqual(spy.createBatchRecord, wantRootBatchRecord) {
		t.Fatalf("CreateBatch record = %+v, want %+v", spy.createBatchRecord, wantRootBatchRecord)
	}
	engineBatchRecord.Jobs[0].Job.Payload[0] = '!'
	if got := string(spy.createBatchRecord.Jobs[0].Job.Payload); got != `{"batch":true}` {
		t.Fatalf("CreateBatch payload aliased engine bytes: %q", got)
	}

	if err := adapter.MarkBatchJobStarted(ctx, "batch-start", "job-start"); err != startedErr || spy.startedBatchID != "batch-start" || spy.startedJobID != "job-start" {
		t.Fatalf("MarkBatchJobStarted result = err:%v args:%q/%q", err, spy.startedBatchID, spy.startedJobID)
	}
	gotSucceeded, succeededDone, err := adapter.MarkBatchJobSucceeded(ctx, "batch-succeed", "job-succeed")
	if err != succeededErr || !succeededDone || spy.succeededBatchID != "batch-succeed" || spy.succeededJobID != "job-succeed" {
		t.Fatalf("MarkBatchJobSucceeded result = state:%+v done:%t err:%v args:%q/%q", gotSucceeded, succeededDone, err, spy.succeededBatchID, spy.succeededJobID)
	}
	assertWorkflowBatchStateMatchesRoot(t, gotSucceeded, batchState)
	gotFailed, failedDone, err := adapter.MarkBatchJobFailed(ctx, "batch-fail", "job-fail", memberCause)
	if err != failedErr || failedDone || spy.failedBatchID != "batch-fail" || spy.failedJobID != "job-fail" || spy.failedCause != memberCause {
		t.Fatalf("MarkBatchJobFailed result = state:%+v done:%t err:%v args:%q/%q cause:%v", gotFailed, failedDone, err, spy.failedBatchID, spy.failedJobID, spy.failedCause)
	}
	assertWorkflowBatchStateMatchesRoot(t, gotFailed, batchState)
	if err := adapter.CancelBatch(ctx, "batch-cancel"); err != cancelErr || spy.cancelBatchID != "batch-cancel" {
		t.Fatalf("CancelBatch result = err:%v id:%q", err, spy.cancelBatchID)
	}
	gotBatch, err := adapter.GetBatch(ctx, "batch-get")
	if err != getBatchErr || spy.getBatchID != "batch-get" {
		t.Fatalf("GetBatch result = err:%v id:%q", err, spy.getBatchID)
	}
	assertWorkflowBatchStateMatchesRoot(t, gotBatch, batchState)

	claimed, err := adapter.MarkCallbackInvoked(ctx, "callback-key")
	if err != callbackErr || !claimed || spy.callbackKey != "callback-key" {
		t.Fatalf("MarkCallbackInvoked result = claimed:%t err:%v key:%q", claimed, err, spy.callbackKey)
	}
	before := updatedAt.Add(24 * time.Hour)
	if err := adapter.Prune(ctx, before); err != pruneErr || !spy.pruneBefore.Equal(before) {
		t.Fatalf("Prune result = err:%v before:%v", err, spy.pruneBefore)
	}
	if !spy.contextsOK {
		t.Fatal("one or more custom store adapter methods replaced the caller context")
	}
}

// assertWorkflowBatchStateMatchesRoot verifies every aggregate field crosses the physical model boundary.
func assertWorkflowBatchStateMatchesRoot(t *testing.T, got workflow.BatchState, want BatchState) {
	t.Helper()
	if got.BatchID != want.BatchID || got.DispatchID != want.DispatchID || got.Name != want.Name || got.Queue != want.Queue || got.AllowFailed != want.AllowFailed || got.Total != want.Total || got.Pending != want.Pending || got.Processed != want.Processed || got.Failed != want.Failed || got.Cancelled != want.Cancelled || got.Completed != want.Completed || !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("workflow batch state = %+v, want fields from %+v", got, want)
	}
}

// TestWorkflowStoreFromRootSelectsOneBoundary verifies nil, built-in, and custom stores take their intended routes.
func TestWorkflowStoreFromRootSelectsOneBoundary(t *testing.T) {
	if got := workflowStoreFromRoot(nil); got != nil {
		t.Fatalf("nil root store adapted as %T, want nil", got)
	}

	builtIn := NewMemoryStore()
	provider, ok := builtIn.(workflowStoreProvider)
	if !ok {
		t.Fatalf("built-in store %T does not expose its engine store", builtIn)
	}
	if got := workflowStoreFromRoot(builtIn); got != provider.workflowStore() {
		t.Fatalf("built-in store route = %T, want direct engine store %T", got, provider.workflowStore())
	}

	custom := &workflowStoreAdapterSpy{contextsOK: true}
	got := workflowStoreFromRoot(custom)
	adapter, ok := got.(rootWorkflowStoreAdapter)
	if !ok {
		t.Fatalf("custom store route = %T, want rootWorkflowStoreAdapter", got)
	}
	if adapter.store != custom {
		t.Fatalf("custom adapter store = %T, want original %T", adapter.store, custom)
	}

	ctx := context.WithValue(context.Background(), workflowStoreAdapterContextKey{}, "outcome-adapter")
	chainCause := errors.New("chain outcome failed")
	batchCause := errors.New("batch outcome failed")
	outcomeErr := errors.New("outcome store failed")
	capable := &workflowOutcomeStoreSpy{
		workflowStoreAdapterSpy: &workflowStoreAdapterSpy{wantContext: ctx, contextsOK: true},
		failNodeState:           ChainState{ChainID: "chain-outcome", Failed: true},
		failNodeOwned:           true,
		failNodeErr:             outcomeErr,
		settleState:             BatchState{BatchID: "batch-outcome", Processed: 1},
		settleOwned:             false,
		settleErr:               outcomeErr,
	}
	adapted := workflowStoreFromRoot(capable)
	outcomeAdapter, ok := adapted.(rootWorkflowOutcomeStoreAdapter)
	if !ok {
		t.Fatalf("capable custom store route = %T, want rootWorkflowOutcomeStoreAdapter", adapted)
	}
	chainState, owned, err := outcomeAdapter.FailChainNode(ctx, "chain-outcome", "node-outcome", chainCause)
	if err != outcomeErr || !owned || chainState.ChainID != "chain-outcome" || capable.failNodeChainID != "chain-outcome" || capable.failNodeID != "node-outcome" || capable.failNodeCause != chainCause {
		t.Fatalf("FailChainNode result = state:%+v owned:%t err:%v spy:%+v", chainState, owned, err, capable)
	}
	batchState, owned, err := outcomeAdapter.SettleBatchJob(ctx, "batch-outcome", "job-outcome", workflow.BatchJobFailed, batchCause)
	if err != outcomeErr || owned || batchState.BatchID != "batch-outcome" || capable.settleBatchID != "batch-outcome" || capable.settleJobID != "job-outcome" || capable.settleOutcome != BatchJobFailed || capable.settleCause != batchCause {
		t.Fatalf("SettleBatchJob result = state:%+v owned:%t err:%v spy:%+v", batchState, owned, err, capable)
	}
	if !capable.contextsOK {
		t.Fatal("outcome adapter replaced the caller context")
	}
}
