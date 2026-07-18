package queue

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestWorkflowStoreViewConvertsEveryMethod pins the complete internal-to-root built-in store boundary.
func TestWorkflowStoreViewConvertsEveryMethod(t *testing.T) {
	ctx := context.WithValue(context.Background(), workflowStoreAdapterContextKey{}, "view-test")
	createdAt := time.Date(2026, time.July, 18, 12, 13, 14, 15, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	storeErr := errors.New("internal store failed")
	chainCause := errors.New("chain failed")
	memberCause := errors.New("member failed")

	chainState := ChainState{
		ChainID:    "chain-state",
		DispatchID: "dispatch-chain-state",
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
		Total:       7,
		Pending:     3,
		Processed:   4,
		Failed:      2,
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
			Options: StoredJobOptions{Queue: "critical", Retry: 8},
		},
	}
	spy := &workflowStoreAdapterSpy{
		wantContext:     ctx,
		contextsOK:      true,
		createChainErr:  storeErr,
		advanceNext:     nextNode,
		advanceDone:     true,
		advanceErr:      storeErr,
		failChainErr:    storeErr,
		getChainState:   chainState,
		getChainErr:     storeErr,
		createBatchErr:  storeErr,
		startedErr:      storeErr,
		succeededState:  batchState,
		succeededDone:   true,
		succeededErr:    storeErr,
		failedState:     batchState,
		failedDone:      false,
		failedErr:       storeErr,
		cancelErr:       storeErr,
		getBatchState:   batchState,
		getBatchErr:     storeErr,
		callbackClaimed: true,
		callbackErr:     storeErr,
		pruneErr:        storeErr,
	}
	view := &workflowStoreView{store: rootWorkflowStoreAdapter{store: spy}}

	chainRecord := ChainRecord{
		ChainID:    "chain-create",
		DispatchID: "dispatch-chain-create",
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
	wantChainRecord := chainRecord
	wantChainRecord.Nodes = append([]ChainNode(nil), chainRecord.Nodes...)
	wantChainRecord.Nodes[0].Job.Payload = cloneWorkflowPayload(chainRecord.Nodes[0].Job.Payload)
	if err := view.CreateChain(ctx, chainRecord); err != storeErr {
		t.Fatalf("CreateChain error = %v, want exact sentinel", err)
	}
	if !reflect.DeepEqual(spy.createChainRecord, wantChainRecord) {
		t.Fatalf("CreateChain record = %+v, want %+v", spy.createChainRecord, wantChainRecord)
	}
	chainRecord.Nodes[0].Job.Payload[0] = '!'
	if got := string(spy.createChainRecord.Nodes[0].Job.Payload); got != `{"create":true}` {
		t.Fatalf("CreateChain payload aliased root input: %q", got)
	}

	next, done, err := view.AdvanceChain(ctx, "chain-advance", "node-complete")
	if err != storeErr || !done || spy.advanceChainID != "chain-advance" || spy.advanceCompletedNode != "node-complete" || next == nil || !reflect.DeepEqual(*next, *nextNode) {
		t.Fatalf("AdvanceChain result = next:%+v done:%t err:%v args:%q/%q", next, done, err, spy.advanceChainID, spy.advanceCompletedNode)
	}
	next.Job.Payload[0] = '?'
	if got := string(spy.advanceNext.Job.Payload); got != `{"next":true}` {
		t.Fatalf("AdvanceChain payload aliased internal output: %q", got)
	}
	spy.advanceNext = nil
	spy.advanceDone = false
	nilNext, nilDone, nilErr := view.AdvanceChain(ctx, "chain-nil", "node-nil")
	if nilNext != nil || nilDone || nilErr != storeErr {
		t.Fatalf("AdvanceChain nil successor = next:%+v done:%t err:%v", nilNext, nilDone, nilErr)
	}

	if err := view.FailChain(ctx, "chain-fail", chainCause); err != storeErr || spy.failChainID != "chain-fail" || spy.failChainCause != chainCause {
		t.Fatalf("FailChain result = err:%v id:%q cause:%v", err, spy.failChainID, spy.failChainCause)
	}
	gotChain, err := view.GetChain(ctx, "chain-get")
	if err != storeErr || spy.getChainID != "chain-get" || !reflect.DeepEqual(gotChain, chainState) {
		t.Fatalf("GetChain result = state:%+v err:%v id:%q", gotChain, err, spy.getChainID)
	}
	gotChain.Nodes[0].Job.Payload[0] = '#'
	if got := string(spy.getChainState.Nodes[0].Job.Payload); got != `{"state":true}` {
		t.Fatalf("GetChain payload aliased internal state: %q", got)
	}

	batchRecord := BatchRecord{
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
	wantBatchRecord := batchRecord
	wantBatchRecord.Jobs = append([]BatchJob(nil), batchRecord.Jobs...)
	wantBatchRecord.Jobs[0].Job.Payload = cloneWorkflowPayload(batchRecord.Jobs[0].Job.Payload)
	if err := view.CreateBatch(ctx, batchRecord); err != storeErr {
		t.Fatalf("CreateBatch error = %v, want exact sentinel", err)
	}
	if !reflect.DeepEqual(spy.createBatchRecord, wantBatchRecord) {
		t.Fatalf("CreateBatch record = %+v, want %+v", spy.createBatchRecord, wantBatchRecord)
	}
	batchRecord.Jobs[0].Job.Payload[0] = '!'
	if got := string(spy.createBatchRecord.Jobs[0].Job.Payload); got != `{"batch":true}` {
		t.Fatalf("CreateBatch payload aliased root input: %q", got)
	}

	if err := view.MarkBatchJobStarted(ctx, "batch-start", "job-start"); err != storeErr || spy.startedBatchID != "batch-start" || spy.startedJobID != "job-start" {
		t.Fatalf("MarkBatchJobStarted result = err:%v args:%q/%q", err, spy.startedBatchID, spy.startedJobID)
	}
	gotSucceeded, succeededDone, err := view.MarkBatchJobSucceeded(ctx, "batch-succeed", "job-succeed")
	if err != storeErr || !succeededDone || spy.succeededBatchID != "batch-succeed" || spy.succeededJobID != "job-succeed" || !reflect.DeepEqual(gotSucceeded, batchState) {
		t.Fatalf("MarkBatchJobSucceeded result = state:%+v done:%t err:%v args:%q/%q", gotSucceeded, succeededDone, err, spy.succeededBatchID, spy.succeededJobID)
	}
	gotFailed, failedDone, err := view.MarkBatchJobFailed(ctx, "batch-fail", "job-fail", memberCause)
	if err != storeErr || failedDone || spy.failedBatchID != "batch-fail" || spy.failedJobID != "job-fail" || spy.failedCause != memberCause || !reflect.DeepEqual(gotFailed, batchState) {
		t.Fatalf("MarkBatchJobFailed result = state:%+v done:%t err:%v args:%q/%q cause:%v", gotFailed, failedDone, err, spy.failedBatchID, spy.failedJobID, spy.failedCause)
	}
	if err := view.CancelBatch(ctx, "batch-cancel"); err != storeErr || spy.cancelBatchID != "batch-cancel" {
		t.Fatalf("CancelBatch result = err:%v id:%q", err, spy.cancelBatchID)
	}
	gotBatch, err := view.GetBatch(ctx, "batch-get")
	if err != storeErr || spy.getBatchID != "batch-get" || !reflect.DeepEqual(gotBatch, batchState) {
		t.Fatalf("GetBatch result = state:%+v err:%v id:%q", gotBatch, err, spy.getBatchID)
	}

	claimed, err := view.MarkCallbackInvoked(ctx, "callback-key")
	if err != storeErr || !claimed || spy.callbackKey != "callback-key" {
		t.Fatalf("MarkCallbackInvoked result = claimed:%t err:%v key:%q", claimed, err, spy.callbackKey)
	}
	before := updatedAt.Add(24 * time.Hour)
	if err := view.Prune(ctx, before); err != storeErr || !spy.pruneBefore.Equal(before) {
		t.Fatalf("Prune result = err:%v before:%v", err, spy.pruneBefore)
	}
	if !spy.contextsOK {
		t.Fatal("one or more built-in store view methods replaced the caller context")
	}
}

// TestWorkflowStoreViewExposesOutcomeCapability proves built-in stores retain
// first-writer arbitration through the root-owned physical model.
func TestWorkflowStoreViewExposesOutcomeCapability(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	outcomes, ok := store.(WorkflowOutcomeStore)
	if !ok {
		t.Fatalf("built-in store %T does not implement WorkflowOutcomeStore", store)
	}
	if err := store.CreateChain(ctx, ChainRecord{ChainID: "chain-outcome-view", Nodes: []ChainNode{{NodeID: "node-outcome-view"}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	chainState, owned, err := outcomes.FailChainNode(ctx, "chain-outcome-view", "node-outcome-view", errors.New("chain failed"))
	if err != nil || !owned || !chainState.Failed || chainState.ChainID != "chain-outcome-view" {
		t.Fatalf("chain outcome = state:%+v owned:%t err:%v", chainState, owned, err)
	}
	if err := store.CreateBatch(ctx, BatchRecord{BatchID: "batch-outcome-view", Jobs: []BatchJob{{JobID: "job-outcome-view"}}}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	batchState, owned, err := outcomes.SettleBatchJob(ctx, "batch-outcome-view", "job-outcome-view", BatchJobSucceeded, nil)
	if err != nil || !owned || !batchState.Completed || batchState.Processed != 1 {
		t.Fatalf("batch outcome = state:%+v owned:%t err:%v", batchState, owned, err)
	}
}
