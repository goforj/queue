package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/goforj/queue/internal/workflow"
)

// TestFakeWorkflowRecorderRejectsInvalidState verifies malformed records cannot
// become visible even when the recorder is used at its storage boundary.
func TestFakeWorkflowRecorderRejectsInvalidState(t *testing.T) {
	fake := NewFake()
	recorder := fake.state.workflow
	ctx := context.Background()

	if err := recorder.CreateChain(ctx, workflow.ChainRecord{}); err == nil {
		t.Fatal("CreateChain error = nil, want invalid record rejection")
	}
	if err := recorder.CreateBatch(ctx, workflow.BatchRecord{}); err == nil {
		t.Fatal("CreateBatch error = nil, want invalid record rejection")
	}

	recorder.acceptChain("")
	recorder.acceptBatch("")
	if got := len(fake.ChainRecords()); got != 0 {
		t.Fatalf("invalid chain records = %d, want 0", got)
	}
	if got := len(fake.BatchRecords()); got != 0 {
		t.Fatalf("invalid batch records = %d, want 0", got)
	}
	fake.AssertNothingBatched(t)
}

// TestFakeWorkflowRecorderChainLifecycle verifies retry-safe advancement,
// isolated successor data, terminal success, and compatibility failure state.
func TestFakeWorkflowRecorderChainLifecycle(t *testing.T) {
	fake := NewFake()
	ctx := context.Background()
	chainID, err := fake.Chain(
		NewJob("chain:first").Payload(json.RawMessage(`{"step":1}`)),
		NewJob("chain:second").Payload(json.RawMessage(`{"step":2}`)),
	).Dispatch(ctx)
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	record := fake.ChainRecords()[0]
	recorder := fake.state.workflow

	next, done, err := recorder.AdvanceChain(ctx, chainID, record.Nodes[0].NodeID)
	if err != nil || done || next == nil || next.NodeID != record.Nodes[1].NodeID {
		t.Fatalf("first advance = next:%+v done:%t err:%v", next, done, err)
	}
	next.Job.Payload[0] = 'x'
	state, err := recorder.GetChain(ctx, chainID)
	if err != nil || state.NextIndex != 1 || string(state.Nodes[1].Job.Payload) != `{"step":2}` {
		t.Fatalf("state after successor mutation = %+v, %v", state, err)
	}

	replayed, done, err := recorder.AdvanceChain(ctx, chainID, record.Nodes[0].NodeID)
	if err != nil || done || replayed == nil || replayed.NodeID != record.Nodes[1].NodeID {
		t.Fatalf("replayed advance = next:%+v done:%t err:%v", replayed, done, err)
	}
	state, err = recorder.GetChain(ctx, chainID)
	if err != nil || state.NextIndex != 1 {
		t.Fatalf("state after replayed advance = %+v, %v", state, err)
	}

	next, done, err = recorder.AdvanceChain(ctx, chainID, record.Nodes[1].NodeID)
	if err != nil || !done || next != nil {
		t.Fatalf("terminal advance = next:%+v done:%t err:%v", next, done, err)
	}
	if err := recorder.FailChain(ctx, chainID, errors.New("late failure")); err != nil {
		t.Fatalf("fail completed chain: %v", err)
	}
	state, err = recorder.GetChain(ctx, chainID)
	if err != nil || !state.Completed || state.Failed || state.Failure != "" {
		t.Fatalf("completed state after late failure = %+v, %v", state, err)
	}

	failedID, err := fake.Chain(NewJob("chain:failed")).Dispatch(ctx)
	if err != nil {
		t.Fatalf("dispatch failing chain: %v", err)
	}
	cause := errors.New("handler failed")
	if err := recorder.FailChain(ctx, failedID, cause); err != nil {
		t.Fatalf("fail active chain: %v", err)
	}
	failed, err := recorder.GetChain(ctx, failedID)
	if err != nil || !failed.Failed || failed.Completed || failed.Failure != cause.Error() {
		t.Fatalf("failed chain state = %+v, %v", failed, err)
	}

	if next, done, err := recorder.AdvanceChain(ctx, "missing-chain", "missing-node"); !errors.Is(err, ErrWorkflowNotFound) || done || next != nil {
		t.Fatalf("missing chain advance = next:%+v done:%t err:%v", next, done, err)
	}
	if err := recorder.FailChain(ctx, "missing-chain", cause); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("missing chain failure error = %v, want ErrWorkflowNotFound", err)
	}
}

// TestFakeWorkflowRecorderBatchLifecycle verifies started markers, idempotent
// aggregate counters, allowed failures, and first-writer outcome ownership.
func TestFakeWorkflowRecorderBatchLifecycle(t *testing.T) {
	fake := NewFake()
	ctx := context.Background()
	batchID, err := fake.Batch(
		NewJob("batch:first"),
		NewJob("batch:second"),
		NewJob("batch:third"),
	).AllowFailures().Dispatch(ctx)
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	record := fake.BatchRecords()[0]
	recorder := fake.state.workflow

	if err := recorder.MarkBatchJobStarted(ctx, batchID, record.Jobs[0].JobID); err != nil {
		t.Fatalf("mark first member started: %v", err)
	}
	if err := recorder.MarkBatchJobStarted(ctx, batchID, "missing-job"); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("missing member start error = %v, want ErrWorkflowNotFound", err)
	}

	state, done, err := recorder.MarkBatchJobSucceeded(ctx, batchID, record.Jobs[0].JobID)
	if err != nil || done || state.Processed != 1 || state.Pending != 2 || state.Failed != 0 {
		t.Fatalf("first success = state:%+v done:%t err:%v", state, done, err)
	}
	replayed, done, err := recorder.MarkBatchJobSucceeded(ctx, batchID, record.Jobs[0].JobID)
	if err != nil || done || replayed.Processed != 1 || replayed.Pending != 2 || replayed.Failed != 0 {
		t.Fatalf("replayed success = state:%+v done:%t err:%v", replayed, done, err)
	}

	cause := errors.New("member failed")
	state, done, err = recorder.MarkBatchJobFailed(ctx, batchID, record.Jobs[1].JobID, cause)
	if err != nil || done || state.Processed != 2 || state.Pending != 1 || state.Failed != 1 || state.Cancelled {
		t.Fatalf("allowed failure = state:%+v done:%t err:%v", state, done, err)
	}

	state, owned, err := recorder.SettleBatchJob(ctx, batchID, record.Jobs[2].JobID, workflow.BatchJobSucceeded, nil)
	if err != nil || !owned || !state.Completed || state.Cancelled || state.Processed != 3 || state.Pending != 0 || state.Failed != 1 {
		t.Fatalf("terminal settlement = state:%+v owned:%t err:%v", state, owned, err)
	}
	state, owned, err = recorder.SettleBatchJob(ctx, batchID, record.Jobs[2].JobID, workflow.BatchJobFailed, cause)
	if err != nil || owned || !state.Completed || state.Processed != 3 || state.Pending != 0 || state.Failed != 1 {
		t.Fatalf("contradictory settlement = state:%+v owned:%t err:%v", state, owned, err)
	}
	if _, _, err := recorder.SettleBatchJob(ctx, batchID, record.Jobs[2].JobID, workflow.BatchJobOutcome("unknown"), nil); err == nil {
		t.Fatal("unknown batch outcome error = nil")
	}

	claimed, err := recorder.MarkCallbackInvoked(ctx, "batch:finally:"+batchID)
	if err != nil || !claimed {
		t.Fatalf("first callback claim = %t, %v", claimed, err)
	}
	claimed, err = recorder.MarkCallbackInvoked(ctx, "batch:finally:"+batchID)
	if err != nil || claimed {
		t.Fatalf("replayed callback claim = %t, %v", claimed, err)
	}
}

// TestFakeWorkflowAssertionsSearchAcceptedRecords verifies assertion helpers
// search all immutable records instead of requiring the first record to match.
func TestFakeWorkflowAssertionsSearchAcceptedRecords(t *testing.T) {
	fake := NewFake()
	ctx := context.Background()
	if _, err := fake.Chain(NewJob("chain:unrelated")).Dispatch(ctx); err != nil {
		t.Fatalf("dispatch unrelated chain: %v", err)
	}
	if _, err := fake.Chain(NewJob("chain:first"), NewJob("chain:second")).Dispatch(ctx); err != nil {
		t.Fatalf("dispatch expected chain: %v", err)
	}
	if _, err := fake.Batch(NewJob("batch:unrelated")).Name("unrelated").Dispatch(ctx); err != nil {
		t.Fatalf("dispatch unrelated batch: %v", err)
	}
	if _, err := fake.Batch(NewJob("batch:expected")).Name("expected").Dispatch(ctx); err != nil {
		t.Fatalf("dispatch expected batch: %v", err)
	}

	fake.AssertChained(t, []string{"chain:first", "chain:second"})
	fake.AssertBatchCount(t, 2)
	fake.AssertBatched(t, func(record BatchRecord) bool {
		return record.Name == "expected"
	})
	if fakeChainTypesEqual(fake.ChainRecords()[1], []string{"chain:wrong", "chain:second"}) {
		t.Fatal("chain type comparison accepted a mismatched member")
	}
}

// TestWithFakeWorkflowDispatchNormalizesNilContext verifies internally emitted
// workflow envelopes remain suppressed when callers omit a context.
func TestWithFakeWorkflowDispatchNormalizesNilContext(t *testing.T) {
	ctx := withFakeWorkflowDispatch(nil)
	if ctx == nil {
		t.Fatal("withFakeWorkflowDispatch(nil) returned nil")
	}
	if !fakeWorkflowDeliverySuppressed(ctx, workflow.ChainNodeDeliveryType) {
		t.Fatal("marked chain delivery was not suppressed")
	}
}
