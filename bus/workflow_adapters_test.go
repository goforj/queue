package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/workflow"
)

type workflowAdapterStoreStub struct {
	Store
	advanceNode    *queue.ChainNode
	advanceDone    bool
	advanceErr     error
	cancelBatchID  string
	cancelBatchErr error
}

type workflowOutcomeAdapterStoreStub struct {
	*workflowAdapterStoreStub
	chainState   queue.ChainState
	chainOwned   bool
	chainErr     error
	batchState   queue.BatchState
	batchOwned   bool
	batchErr     error
	batchOutcome queue.BatchJobOutcome
}

// FailChainNode returns the configured root outcome for adapter conversion.
func (s *workflowOutcomeAdapterStoreStub) FailChainNode(context.Context, string, string, error) (queue.ChainState, bool, error) {
	return s.chainState, s.chainOwned, s.chainErr
}

// SettleBatchJob records the converted outcome and returns configured state.
func (s *workflowOutcomeAdapterStoreStub) SettleBatchJob(_ context.Context, _, _ string, outcome queue.BatchJobOutcome, _ error) (queue.BatchState, bool, error) {
	s.batchOutcome = outcome
	return s.batchState, s.batchOwned, s.batchErr
}

// AdvanceChain returns the configured successor and outcome for adapter boundary tests.
func (s *workflowAdapterStoreStub) AdvanceChain(context.Context, string, string) (*queue.ChainNode, bool, error) {
	return s.advanceNode, s.advanceDone, s.advanceErr
}

// CancelBatch records the aggregate identifier and returns the configured error.
func (s *workflowAdapterStoreStub) CancelBatch(_ context.Context, batchID string) error {
	s.cancelBatchID = batchID
	return s.cancelBatchErr
}

// TestWorkflowMessageAdaptersPreserveMetadataAndPayload pins both raw-route directions.
func TestWorkflowMessageAdaptersPreserveMetadataAndPayload(t *testing.T) {
	payload := []byte(`{"id":7}`)
	engineMessage := workflow.NewContext(
		1,
		"dispatch-1",
		"job-1",
		"chain-1",
		"batch-1",
		3,
		"reports:build",
		payload,
	)
	payload[0] = '!'

	rootMessage := toQueueMessage(engineMessage)
	if rootMessage.SchemaVersion != 1 || rootMessage.DispatchID != "dispatch-1" || rootMessage.JobID != "job-1" || rootMessage.ChainID != "chain-1" || rootMessage.BatchID != "batch-1" || rootMessage.Attempt != 3 || rootMessage.JobType != "reports:build" {
		t.Fatalf("root message metadata changed: %+v", rootMessage)
	}
	if got := string(rootMessage.PayloadBytes()); got != `{"id":7}` {
		t.Fatalf("root message payload = %q, want preserved JSON", got)
	}

	rootMessage.JobType = "reports:replace"
	roundTrip := toWorkflowContext(rootMessage)
	if roundTrip.JobType != "reports:replace" || roundTrip.DispatchID != "dispatch-1" || roundTrip.Attempt != 3 {
		t.Fatalf("engine message round trip changed: %+v", roundTrip)
	}
	returnedPayload := roundTrip.PayloadBytes()
	returnedPayload[0] = '?'
	if got := string(roundTrip.PayloadBytes()); got != `{"id":7}` {
		t.Fatalf("engine message payload was not isolated: %q", got)
	}
}

// TestWorkflowMiddlewareAdapterPreservesMessageReplacement verifies the continuation crosses both physical models.
func TestWorkflowMiddlewareAdapterPreservesMessageReplacement(t *testing.T) {
	adapter := workflowMiddlewareAdapter{middleware: MiddlewareFunc(func(ctx context.Context, _ Context, next Next) error {
		replacement := queue.NewMessage("reports:replacement", []byte(`{"replacement":true}`))
		replacement.SchemaVersion = 1
		replacement.DispatchID = "dispatch-replacement"
		return next(ctx, replacement)
	})}

	var received workflow.Context
	err := adapter.Handle(context.Background(), workflow.NewContext(1, "dispatch-original", "job-1", "", "", 0, "reports:original", []byte(`null`)), func(_ context.Context, message workflow.Context) error {
		received = message
		return nil
	})
	if err != nil {
		t.Fatalf("handle middleware: %v", err)
	}
	if received.JobType != "reports:replacement" || received.DispatchID != "dispatch-replacement" || string(received.PayloadBytes()) != `{"replacement":true}` {
		t.Fatalf("replacement message changed across adapter: %+v payload=%q", received, received.PayloadBytes())
	}
}

// TestWorkflowRecordAdaptersPreservePhysicalShapes verifies nested jobs, policy, times, and byte ownership.
func TestWorkflowRecordAdaptersPreservePhysicalShapes(t *testing.T) {
	createdAt := time.Unix(1_704_067_200, 123_000_000)
	engineChain := workflow.ChainRecord{
		ChainID:    "chain-1",
		DispatchID: "dispatch-1",
		Queue:      "critical",
		Nodes: []workflow.ChainNode{{
			NodeID: "node-1",
			Job: workflow.StoredJob{
				Type:    "reports:build",
				Payload: []byte(`{"id":7}`),
				Options: workflow.JobOptions{Queue: "critical", Delay: time.Second, Timeout: 2 * time.Second, Retry: 3, Backoff: 4 * time.Second, UniqueFor: 5 * time.Second},
			},
		}},
		CreatedAt: createdAt,
	}
	rootChain := toQueueChainRecord(engineChain)
	if rootChain.ChainID != "chain-1" || rootChain.DispatchID != "dispatch-1" || rootChain.Queue != "critical" || !rootChain.CreatedAt.Equal(createdAt) || len(rootChain.Nodes) != 1 {
		t.Fatalf("root chain record changed: %+v", rootChain)
	}
	job := rootChain.Nodes[0].Job
	if job.Type != "reports:build" || string(job.Payload) != `{"id":7}` || job.Options.Delay != time.Second || job.Options.Timeout != 2*time.Second || job.Options.Retry != 3 || job.Options.Backoff != 4*time.Second || job.Options.UniqueFor != 5*time.Second {
		t.Fatalf("root stored job changed: %+v", job)
	}
	rootChain.Nodes[0].Job.Payload[0] = '!'
	if got := string(engineChain.Nodes[0].Job.Payload); got != `{"id":7}` {
		t.Fatalf("engine payload aliased root payload: %q", got)
	}

	rootBatch := queue.BatchState{
		BatchID:     "batch-1",
		DispatchID:  "dispatch-2",
		Name:        "nightly",
		Queue:       "bulk",
		AllowFailed: true,
		Total:       8,
		Pending:     3,
		Processed:   5,
		Failed:      2,
		Cancelled:   false,
		Completed:   false,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt.Add(time.Minute),
	}
	engineBatch := toWorkflowBatchState(rootBatch)
	if engineBatch.BatchID != rootBatch.BatchID || engineBatch.DispatchID != rootBatch.DispatchID || engineBatch.Name != rootBatch.Name || engineBatch.Queue != rootBatch.Queue || engineBatch.AllowFailed != rootBatch.AllowFailed || engineBatch.Total != rootBatch.Total || engineBatch.Pending != rootBatch.Pending || engineBatch.Processed != rootBatch.Processed || engineBatch.Failed != rootBatch.Failed || engineBatch.Cancelled != rootBatch.Cancelled || engineBatch.Completed != rootBatch.Completed || !engineBatch.CreatedAt.Equal(rootBatch.CreatedAt) || !engineBatch.UpdatedAt.Equal(rootBatch.UpdatedAt) {
		t.Fatalf("engine batch state changed: %+v", engineBatch)
	}
}

// TestWorkflowStoreAdapterAdvanceChainCoversSuccessorBranches pins optional-node conversion and error identity.
func TestWorkflowStoreAdapterAdvanceChainCoversSuccessorBranches(t *testing.T) {
	sentinel := errors.New("advance chain failed")
	tests := []struct {
		name     string
		node     *queue.ChainNode
		done     bool
		err      error
		wantNode bool
	}{
		{name: "nil successor", done: true},
		{
			name: "converted successor",
			node: &queue.ChainNode{
				NodeID: "node-2",
				Job: queue.StoredJob{
					Type:    "reports:publish",
					Payload: []byte(`{"id":8}`),
					Options: queue.StoredJobOptions{
						Queue:     "critical",
						Delay:     time.Second,
						Timeout:   2 * time.Second,
						Retry:     3,
						Backoff:   4 * time.Second,
						UniqueFor: 5 * time.Second,
					},
				},
			},
			wantNode: true,
		},
		{
			name: "successor with store error",
			node: &queue.ChainNode{
				NodeID: "node-error",
				Job:    queue.StoredJob{Type: "reports:error", Payload: []byte(`null`)},
			},
			err:      sentinel,
			wantNode: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &workflowAdapterStoreStub{
				advanceNode: test.node,
				advanceDone: test.done,
				advanceErr:  test.err,
			}
			adapted := toWorkflowStore(store)
			node, done, err := adapted.AdvanceChain(context.Background(), "chain-1", "node-1")
			if done != test.done {
				t.Fatalf("done = %t, want %t", done, test.done)
			}
			if err != test.err {
				t.Fatalf("error = %v, want exact identity %v", err, test.err)
			}
			if (node != nil) != test.wantNode {
				t.Fatalf("successor = %+v, want present=%t", node, test.wantNode)
			}
			if node == nil {
				return
			}
			if node.NodeID != test.node.NodeID || node.Job.Type != test.node.Job.Type || string(node.Job.Payload) != string(test.node.Job.Payload) {
				t.Fatalf("converted successor = %+v, want %+v", node, test.node)
			}
			if node.Job.Options.Queue != test.node.Job.Options.Queue || node.Job.Options.Delay != test.node.Job.Options.Delay || node.Job.Options.Timeout != test.node.Job.Options.Timeout || node.Job.Options.Retry != test.node.Job.Options.Retry || node.Job.Options.Backoff != test.node.Job.Options.Backoff || node.Job.Options.UniqueFor != test.node.Job.Options.UniqueFor {
				t.Fatalf("converted successor options = %+v, want %+v", node.Job.Options, test.node.Job.Options)
			}
			if len(node.Job.Payload) > 0 {
				node.Job.Payload[0] = '!'
				if test.node.Job.Payload[0] == '!' {
					t.Fatal("converted successor payload aliases the physical store value")
				}
			}
		})
	}

	if got := toWorkflowStore(nil); got != nil {
		t.Fatalf("nil store adapted to %T, want nil", got)
	}
}

// TestWorkflowStoreAdapterCancelBatchPreservesIDAndError verifies cancellation forwarding without error wrapping.
func TestWorkflowStoreAdapterCancelBatchPreservesIDAndError(t *testing.T) {
	sentinel := errors.New("cancel batch failed")
	store := &workflowAdapterStoreStub{cancelBatchErr: sentinel}
	adapted := toWorkflowStore(store)

	err := adapted.CancelBatch(context.Background(), "batch-7")
	if err != sentinel {
		t.Fatalf("cancel error = %v, want exact identity %v", err, sentinel)
	}
	if store.cancelBatchID != "batch-7" {
		t.Fatalf("cancelled batch = %q, want batch-7", store.cancelBatchID)
	}
}

// TestWorkflowOutcomeStoreAdapterPreservesOwnership proves the deprecated raw
// route forwards the one canonical root capability instead of redefining it.
func TestWorkflowOutcomeStoreAdapterPreservesOwnership(t *testing.T) {
	chainErr := errors.New("chain outcome failed")
	batchErr := errors.New("batch outcome failed")
	store := &workflowOutcomeAdapterStoreStub{
		workflowAdapterStoreStub: &workflowAdapterStoreStub{},
		chainState:               queue.ChainState{ChainID: "chain-outcome", Failed: true},
		chainOwned:               true,
		chainErr:                 chainErr,
		batchState:               queue.BatchState{BatchID: "batch-outcome", Processed: 1},
		batchOwned:               false,
		batchErr:                 batchErr,
	}
	adapted := toWorkflowStore(store)
	outcomes, ok := adapted.(interface {
		FailChainNode(context.Context, string, string, error) (workflow.ChainState, bool, error)
		SettleBatchJob(context.Context, string, string, workflow.BatchJobOutcome, error) (workflow.BatchState, bool, error)
	})
	if !ok {
		t.Fatalf("capable store adapted as %T without outcome capability", adapted)
	}
	chainState, owned, err := outcomes.FailChainNode(context.Background(), "chain-outcome", "node-outcome", chainErr)
	if err != chainErr || !owned || chainState.ChainID != "chain-outcome" || !chainState.Failed {
		t.Fatalf("chain outcome = state:%+v owned:%t err:%v", chainState, owned, err)
	}
	batchState, owned, err := outcomes.SettleBatchJob(context.Background(), "batch-outcome", "job-outcome", workflow.BatchJobFailed, batchErr)
	if err != batchErr || owned || batchState.BatchID != "batch-outcome" || store.batchOutcome != queue.BatchJobFailed {
		t.Fatalf("batch outcome = state:%+v owned:%t err:%v stored:%q", batchState, owned, err, store.batchOutcome)
	}
}
