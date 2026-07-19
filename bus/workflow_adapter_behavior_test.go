package bus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
)

// adapterBranchInboundJob keeps the raw-runtime test at the same serialized
// boundary used by real queue adapters.
type adapterBranchInboundJob struct {
	payload []byte
}

// Bind decodes one physical delivery for the workflow engine.
func (j adapterBranchInboundJob) Bind(dst any) error {
	return json.Unmarshal(j.payload, dst)
}

// PayloadBytes returns an isolated view of the physical delivery.
func (j adapterBranchInboundJob) PayloadBytes() []byte {
	return append([]byte(nil), j.payload...)
}

// adapterBranchRuntime executes registered deliveries synchronously so callback
// conversion is observed rather than merely retained in process-local state.
type adapterBranchRuntime struct {
	handlers map[string]busruntime.Handler
}

// BusRegister records a workflow delivery handler.
func (r *adapterBranchRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if r.handlers == nil {
		r.handlers = make(map[string]busruntime.Handler)
	}
	r.handlers[jobType] = handler
}

// BusDispatch invokes the registered delivery at the serialized runtime seam.
func (r *adapterBranchRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, _ busruntime.JobOptions) error {
	handler := r.handlers[jobType]
	if handler == nil {
		return errors.New("adapter branch handler is not registered")
	}
	return handler(ctx, adapterBranchInboundJob{payload: append([]byte(nil), payload...)})
}

// StartWorkers is inert because this test runtime executes synchronously.
func (r *adapterBranchRuntime) StartWorkers(context.Context) error {
	return nil
}

// Shutdown is inert because this test runtime owns no asynchronous work.
func (r *adapterBranchRuntime) Shutdown(context.Context) error {
	return nil
}

// adapterBranchStore records compatibility-store calls whose fallback methods
// are bypassed when the additive atomic store capability is present.
type adapterBranchStore struct {
	Store
	failChainID string
	failCause   error
	failErr     error

	successBatchID string
	successJobID   string
	successState   queue.BatchState
	successDone    bool
	successErr     error

	failureBatchID string
	failureJobID   string
	failureCause   error
	failureState   queue.BatchState
	failureDone    bool
	failureErr     error
}

// FailChain records the cause without changing its identity.
func (s *adapterBranchStore) FailChain(_ context.Context, chainID string, cause error) error {
	s.failChainID = chainID
	s.failCause = cause
	return s.failErr
}

// MarkBatchJobSucceeded returns configured legacy aggregate state.
func (s *adapterBranchStore) MarkBatchJobSucceeded(_ context.Context, batchID, jobID string) (queue.BatchState, bool, error) {
	s.successBatchID = batchID
	s.successJobID = jobID
	return s.successState, s.successDone, s.successErr
}

// MarkBatchJobFailed records the delivery-local cause and returns configured state.
func (s *adapterBranchStore) MarkBatchJobFailed(_ context.Context, batchID, jobID string, cause error) (queue.BatchState, bool, error) {
	s.failureBatchID = batchID
	s.failureJobID = jobID
	s.failureCause = cause
	return s.failureState, s.failureDone, s.failureErr
}

// TestRawRuntimeAdapterRejectsUnsupportedInputs preserves actionable errors at
// both compatibility construction boundaries.
func TestRawRuntimeAdapterRejectsUnsupportedInputs(t *testing.T) {
	if compatibility, err := NewWithStore((*queue.Queue)(nil), NewMemoryStore()); compatibility != nil || err == nil || err.Error() != "queue is required" {
		t.Fatalf("typed nil queue construction = bus:%v err:%v, want nil/queue is required", compatibility, err)
	}
	if compatibility, err := New(struct{}{}); compatibility != nil || err == nil || err.Error() != "queue does not support bus runtime adapter" {
		t.Fatalf("unsupported runtime construction = bus:%v err:%v", compatibility, err)
	}
}

// TestRawRuntimeBatchProgressConvertsCommittedState proves a non-nil legacy
// progress callback observes the canonical engine state after member settlement.
func TestRawRuntimeBatchProgressConvertsCommittedState(t *testing.T) {
	runtime := &adapterBranchRuntime{}
	compatibility, err := New(runtime)
	if err != nil {
		t.Fatalf("new raw runtime adapter: %v", err)
	}
	compatibility.Register("adapter:batch-progress", func(context.Context, Context) error { return nil })

	var (
		progressCalls int
		progressState BatchState
	)
	batchID, err := compatibility.Batch(NewJob("adapter:batch-progress", map[string]int{"id": 7})).
		Progress(func(_ context.Context, state BatchState) error {
			progressCalls++
			progressState = state
			return nil
		}).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	if progressCalls != 1 || progressState.BatchID != batchID || !progressState.Completed || progressState.Pending != 0 || progressState.Processed != 1 {
		t.Fatalf("progress calls/state = %d/%+v, want one completed member", progressCalls, progressState)
	}
}

// TestWorkflowAdapterFallbackPreservesNilShapesAndOutcomes verifies legacy
// stores retain nil collection identity, state conversion, and error identity.
func TestWorkflowAdapterFallbackPreservesNilShapesAndOutcomes(t *testing.T) {
	if cloneStoredPayload(nil) != nil {
		t.Fatal("nil stored payload became a non-nil slice")
	}
	if toQueueBatchJobs(nil) != nil {
		t.Fatal("nil batch jobs became a non-nil slice")
	}
	if toWorkflowMiddlewares(nil) != nil {
		t.Fatal("nil middleware list became a non-nil slice")
	}

	failCause := errors.New("chain failed")
	failErr := errors.New("chain store unavailable")
	successErr := errors.New("success readback unavailable")
	failureCause := errors.New("member failed")
	failureErr := errors.New("failure readback unavailable")
	store := &adapterBranchStore{
		failErr:      failErr,
		successState: queue.BatchState{BatchID: "batch-success", Processed: 1, Completed: true},
		successDone:  true,
		successErr:   successErr,
		failureState: queue.BatchState{BatchID: "batch-failure", Processed: 1, Failed: 1, Completed: true},
		failureDone:  true,
		failureErr:   failureErr,
	}
	adapter := workflowStoreAdapter{store: store}

	if err := adapter.FailChain(context.Background(), "chain-1", failCause); !errors.Is(err, failErr) || store.failChainID != "chain-1" || store.failCause != failCause {
		t.Fatalf("fail chain = id:%q cause:%v err:%v", store.failChainID, store.failCause, err)
	}
	success, done, err := adapter.MarkBatchJobSucceeded(context.Background(), "batch-success", "job-success")
	if !errors.Is(err, successErr) || !done || success.BatchID != "batch-success" || !success.Completed || store.successBatchID != "batch-success" || store.successJobID != "job-success" {
		t.Fatalf("successful member conversion = state:%+v done:%t err:%v store:%q/%q", success, done, err, store.successBatchID, store.successJobID)
	}
	failure, done, err := adapter.MarkBatchJobFailed(context.Background(), "batch-failure", "job-failure", failureCause)
	if !errors.Is(err, failureErr) || !done || failure.BatchID != "batch-failure" || failure.Failed != 1 || !failure.Completed || store.failureBatchID != "batch-failure" || store.failureJobID != "job-failure" || store.failureCause != failureCause {
		t.Fatalf("failed member conversion = state:%+v done:%t err:%v store:%q/%q/%v", failure, done, err, store.failureBatchID, store.failureJobID, store.failureCause)
	}

	if _, ok := toWorkflowStore(store).(workflowOutcomeStoreAdapter); ok {
		t.Fatal("legacy store unexpectedly advertised atomic outcome ownership")
	}
}
