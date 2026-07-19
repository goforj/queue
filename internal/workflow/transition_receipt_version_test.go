package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue/busruntime"
)

// TestUnknownTransitionReceiptVersionsBlockRecoveredApplicationExecution
// proves a mixed-version worker neither acknowledges nor replays application
// code when durable provenance exists but cannot be interpreted.
func TestUnknownTransitionReceiptVersionsBlockRecoveredApplicationExecution(t *testing.T) {
	t.Run("chain receipt version", func(t *testing.T) {
		const (
			chainID    = "chain-unknown-receipt-runtime"
			nodeID     = "node-unknown-receipt-runtime"
			dispatchID = "dispatch-unknown-receipt-runtime"
			jobID      = "job-unknown-receipt-runtime"
			jobType    = "workflow:chain:unknown-receipt-runtime"
			owner      = "generation-chain-unknown-receipt"
		)
		store := NewMemoryStore().(*memoryStore)
		env := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "chain_node", ChainID: chainID, NodeID: nodeID, JobID: jobID, Job: StoredJob{Type: jobType}}
		if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: []ChainNode{{NodeID: nodeID, Job: env.Job}}}); err != nil {
			t.Fatalf("create chain: %v", err)
		}
		if result, err := store.advanceChainOutcome(context.Background(), chainID, nodeID, workflowTransitionClaim(env, 2, owner)); err != nil || !result.receiptKnown {
			t.Fatalf("advance chain = %+v, err:%v", result, err)
		}
		key := transitionReceiptKey{workflowKind: chainTransitionKind, workflowID: chainID, memberID: nodeID}
		store.mu.Lock()
		receipt := store.transitionReceipts[key]
		receipt.version++
		store.transitionReceipts[key] = receipt
		store.mu.Unlock()

		runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
		var handlerCalls int
		runtime.Register(jobType, func(context.Context, Context) error {
			handlerCalls++
			return nil
		})
		deliveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
		deliveryContext = workflowRecoveryContext(deliveryContext, "generation-chain-unknown-replay", owner)
		err := queueRuntime.DispatchJSON(deliveryContext, internalJobChainNode, env)
		if !busruntime.IsUncommitted(err) || !errors.Is(err, errUnsupportedTransitionReceipt) {
			t.Fatalf("unknown chain receipt error = %v", err)
		}
		if handlerCalls != 0 || settlement.ApplicationStateCommitted() || len(recorder.events) != 0 {
			t.Fatalf("chain handler/committed/events = %d/%t/%d, want 0/false/0", handlerCalls, settlement.ApplicationStateCommitted(), len(recorder.events))
		}
	})

	t.Run("batch event schema", func(t *testing.T) {
		const (
			batchID    = "batch-unknown-receipt-runtime"
			dispatchID = "dispatch-batch-unknown-receipt-runtime"
			jobID      = "job-batch-unknown-receipt-runtime"
			jobType    = "workflow:batch:unknown-receipt-runtime"
			owner      = "generation-batch-unknown-receipt"
		)
		store := NewMemoryStore().(*memoryStore)
		env := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "batch_job", BatchID: batchID, JobID: jobID, Job: StoredJob{Type: jobType}}
		if err := store.CreateBatch(context.Background(), BatchRecord{BatchID: batchID, DispatchID: dispatchID, Jobs: []BatchJob{{JobID: jobID, Job: env.Job}}}); err != nil {
			t.Fatalf("create batch: %v", err)
		}
		if result, err := store.settleBatchOutcome(context.Background(), batchID, jobID, BatchJobSucceeded, nil, workflowTransitionClaim(env, 2, owner)); err != nil || !result.receiptKnown {
			t.Fatalf("settle batch = %+v, err:%v", result, err)
		}
		key := transitionReceiptKey{workflowKind: batchTransitionKind, workflowID: batchID, memberID: jobID}
		store.mu.Lock()
		receipt := store.transitionReceipts[key]
		receipt.eventSchemaVersion++
		store.transitionReceipts[key] = receipt
		store.mu.Unlock()

		runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
		var handlerCalls int
		runtime.Register(jobType, func(context.Context, Context) error {
			handlerCalls++
			return nil
		})
		deliveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
		deliveryContext = workflowRecoveryContext(deliveryContext, "generation-batch-unknown-replay", owner)
		err := queueRuntime.DispatchJSON(deliveryContext, internalJobBatchJob, env)
		if !busruntime.IsUncommitted(err) || !errors.Is(err, errUnsupportedTransitionReceipt) {
			t.Fatalf("unknown batch receipt error = %v", err)
		}
		if handlerCalls != 0 || settlement.ApplicationStateCommitted() || len(recorder.events) != 0 {
			t.Fatalf("batch handler/committed/events = %d/%t/%d, want 0/false/0", handlerCalls, settlement.ApplicationStateCommitted(), len(recorder.events))
		}
	})
}
