package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStoreFactories(t *testing.T) map[string]func(t *testing.T) Store {
	t.Helper()
	return map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store {
			t.Helper()
			return NewMemoryStore()
		},
		"sql_sqlite": func(t *testing.T) Store {
			t.Helper()
			dsn := filepath.Join(t.TempDir(), "store-contract.db") + "?_pragma=busy_timeout%3d5000"
			store, err := NewSQLStore(SQLStoreConfig{
				DriverName: "sqlite",
				DSN:        dsn,
			})
			if err != nil {
				t.Fatalf("new sql store: %v", err)
			}
			t.Cleanup(func() { _ = store.(*sqlStore).db.Close() })
			return store
		},
	}
}

// waitStoreContractOperations bounds lock-sensitive probes so a regression is
// reported by the focused contract rather than the package-wide test timeout.
func waitStoreContractOperations(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for concurrent store operations")
	}
}

// requireOutcomeStore keeps the compatibility Store contract unchanged while
// asserting that every built-in implementation provides stronger arbitration.
func requireOutcomeStore(t *testing.T, store Store) outcomeStore {
	t.Helper()
	outcomes, ok := store.(outcomeStore)
	if !ok {
		t.Fatalf("built-in store %T does not implement outcomeStore", store)
	}
	return outcomes
}

// requireChainAdvanceStore verifies every built-in can distinguish transition
// ownership without changing the public compatibility contract.
func requireChainAdvanceStore(t *testing.T, store Store) chainAdvanceStore {
	t.Helper()
	atomic, ok := store.(chainAdvanceStore)
	if !ok {
		t.Fatalf("built-in store %T does not implement chainAdvanceStore", store)
	}
	return atomic
}

// requireChainFailureStore verifies every built-in can persist terminal
// failure provenance without changing the public compatibility contract.
func requireChainFailureStore(t *testing.T, store Store) chainFailureStore {
	t.Helper()
	atomic, ok := store.(chainFailureStore)
	if !ok {
		t.Fatalf("built-in store %T does not implement chainFailureStore", store)
	}
	return atomic
}

// requireBatchSettlementStore verifies every built-in exposes its exact
// member counter claim for recovery decisions.
func requireBatchSettlementStore(t *testing.T, store Store) batchSettlementStore {
	t.Helper()
	atomic, ok := store.(batchSettlementStore)
	if !ok {
		t.Fatalf("built-in store %T does not implement batchSettlementStore", store)
	}
	return atomic
}

// requireTransitionReceiptStore verifies built-ins expose durable ownership
// without adding receipt methods to the compatibility-critical public store.
func requireTransitionReceiptStore(t *testing.T, store Store) transitionReceiptStore {
	t.Helper()
	receipts, ok := store.(transitionReceiptStore)
	if !ok {
		t.Fatalf("built-in store %T does not implement transitionReceiptStore", store)
	}
	return receipts
}

// TestStoreContract_TransitionOwnership distinguishes first claims from
// same-category and contradictory physical replays across every built-in store.
func TestStoreContract_TransitionOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			chainStore := requireChainAdvanceStore(t, store)
			chainClaim := transitionClaim{deliveryID: "generation-chain", attempt: 0, dispatchID: "dispatch-chain", jobID: "job-chain", jobFingerprint: "fingerprint-chain"}
			if err := store.CreateChain(ctx, ChainRecord{
				ChainID:    "chain-transition-ownership",
				DispatchID: chainClaim.dispatchID,
				Nodes: []ChainNode{
					{NodeID: "node-first"},
					{NodeID: "node-final"},
				},
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			first, err := chainStore.advanceChainOutcome(ctx, "chain-transition-ownership", "node-first", chainClaim)
			if err != nil || !first.claimedNow || !first.successOwned || !first.receiptKnown || first.receipt.owner != chainClaim || first.next == nil || first.next.NodeID != "node-final" {
				t.Fatalf("first chain claim = %+v err:%v", first, err)
			}
			replay, err := chainStore.advanceChainOutcome(ctx, "chain-transition-ownership", "node-first", transitionClaim{deliveryID: "generation-chain-replay", attempt: 0, dispatchID: chainClaim.dispatchID, jobID: chainClaim.jobID, jobFingerprint: chainClaim.jobFingerprint})
			if err != nil || replay.claimedNow || !replay.successOwned || !replay.receiptKnown || replay.receipt.owner != chainClaim || replay.next == nil || replay.next.NodeID != "node-final" {
				t.Fatalf("chain replay = %+v err:%v", replay, err)
			}

			failureClaim := transitionClaim{deliveryID: "generation-chain-failure", attempt: 1, dispatchID: "dispatch-chain-failure", jobID: "job-chain-failure", jobFingerprint: "fingerprint-chain-failure"}
			if err := store.CreateChain(ctx, ChainRecord{
				ChainID:    "chain-failure-transition-ownership",
				DispatchID: failureClaim.dispatchID,
				Nodes:      []ChainNode{{NodeID: "node-failure"}},
			}); err != nil {
				t.Fatalf("create failure chain: %v", err)
			}
			failureStore := requireChainFailureStore(t, store)
			failed, err := failureStore.failChainOutcome(ctx, "chain-failure-transition-ownership", "node-failure", errors.New("committed failure"), failureClaim)
			if err != nil || !failed.claimedNow || !failed.owned || !failed.receiptKnown || failed.receipt.owner != failureClaim || failed.receipt.outcome != BatchJobFailed || failed.receipt.aggregateCompleted || failed.receipt.aggregateCancelled || !failed.state.Failed || failed.state.Completed {
				t.Fatalf("first chain failure claim = %+v err:%v", failed, err)
			}
			failedReplay, err := failureStore.failChainOutcome(ctx, "chain-failure-transition-ownership", "node-failure", errors.New("replacement failure"), transitionClaim{deliveryID: "generation-chain-failure-replay", attempt: 1, dispatchID: failureClaim.dispatchID, jobID: failureClaim.jobID, jobFingerprint: failureClaim.jobFingerprint})
			if err != nil || failedReplay.claimedNow || !failedReplay.owned || !failedReplay.receiptKnown || failedReplay.receipt.owner != failureClaim || failedReplay.state.Failure != "committed failure" {
				t.Fatalf("chain failure replay = %+v err:%v", failedReplay, err)
			}

			batchStore := requireBatchSettlementStore(t, store)
			batchClaim := transitionClaim{deliveryID: "generation-batch", attempt: 0, dispatchID: "dispatch-batch", jobID: "job-first", jobFingerprint: "fingerprint-batch"}
			if err := store.CreateBatch(ctx, BatchRecord{
				BatchID:    "batch-transition-ownership",
				DispatchID: batchClaim.dispatchID,
				Jobs:       []BatchJob{{JobID: "job-first"}, {JobID: "job-final"}},
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			settled, err := batchStore.settleBatchOutcome(ctx, "batch-transition-ownership", "job-first", BatchJobSucceeded, nil, batchClaim)
			if err != nil || !settled.claimedNow || !settled.owned || !settled.receiptKnown || settled.receipt.owner != batchClaim || settled.state.Processed != 1 {
				t.Fatalf("first batch claim = %+v err:%v", settled, err)
			}
			replayed, err := batchStore.settleBatchOutcome(ctx, "batch-transition-ownership", "job-first", BatchJobSucceeded, nil, transitionClaim{deliveryID: "generation-batch-replay", attempt: 0, dispatchID: batchClaim.dispatchID, jobID: batchClaim.jobID, jobFingerprint: batchClaim.jobFingerprint})
			if err != nil || replayed.claimedNow || !replayed.owned || !replayed.receiptKnown || replayed.receipt.owner != batchClaim || replayed.state.Processed != 1 {
				t.Fatalf("same batch replay = %+v err:%v", replayed, err)
			}
			contradictory, err := batchStore.settleBatchOutcome(ctx, "batch-transition-ownership", "job-first", BatchJobFailed, errors.New("contradictory"), transitionClaim{})
			if err != nil || contradictory.claimedNow || contradictory.owned || contradictory.state.Processed != 1 {
				t.Fatalf("contradictory batch replay = %+v err:%v", contradictory, err)
			}
		})
	}
}

// TestStoreContract_TransitionClaimDispatchMismatch proves built-ins reject a
// complete or transport-only claim for another workflow incarnation without mutation.
func TestStoreContract_TransitionClaimDispatchMismatch(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			const (
				chainID = "chain-claim-dispatch-mismatch"
				nodeID  = "node-claim-dispatch-mismatch"
				batchID = "batch-claim-dispatch-mismatch"
				jobID   = "job-claim-dispatch-mismatch"
			)
			if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, DispatchID: "dispatch-current-chain", Nodes: []ChainNode{{NodeID: nodeID}}}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			mismatches := []transitionClaim{
				{deliveryID: "generation-mismatch", attempt: 0, dispatchID: "dispatch-other", jobID: "job-mismatch", jobFingerprint: "fingerprint-mismatch"},
				{dispatchID: "dispatch-other", jobID: "job-mismatch", jobFingerprint: "fingerprint-mismatch"},
			}
			for index, mismatch := range mismatches {
				if _, err := requireChainAdvanceStore(t, store).advanceChainOutcome(ctx, chainID, nodeID, mismatch); err == nil || !strings.Contains(err.Error(), "dispatch mismatch") {
					t.Fatalf("advance mismatch %d error = %v", index, err)
				}
				if _, err := requireChainFailureStore(t, store).failChainOutcome(ctx, chainID, nodeID, errors.New("must not commit"), mismatch); err == nil || !strings.Contains(err.Error(), "dispatch mismatch") {
					t.Fatalf("failure mismatch %d error = %v", index, err)
				}
			}
			chain, err := store.GetChain(ctx, chainID)
			if err != nil || chain.NextIndex != 0 || chain.Completed || chain.Failed {
				t.Fatalf("chain after mismatches = %+v err:%v", chain, err)
			}
			if receipt, known, err := requireTransitionReceiptStore(t, store).chainTransitionReceipt(ctx, chainID, nodeID); err != nil || known {
				t.Fatalf("chain mismatch receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}
			if _, done, err := store.AdvanceChain(ctx, chainID, nodeID); err != nil || !done {
				t.Fatalf("complete current chain = done:%t err:%v", done, err)
			}
			staleSuccess, err := requireChainAdvanceStore(t, store).advanceChainOutcome(ctx, chainID, nodeID, mismatches[1])
			if err != nil || staleSuccess.successOwned || staleSuccess.claimedNow || staleSuccess.receiptKnown || staleSuccess.next != nil || staleSuccess.done {
				t.Fatalf("terminal stale chain success = %+v err:%v, want pure non-owner no-op", staleSuccess, err)
			}
			if err := store.CreateChain(ctx, ChainRecord{ChainID: "chain-failure-terminal-dispatch-mismatch", DispatchID: "dispatch-current-chain", Nodes: []ChainNode{{NodeID: nodeID}}}); err != nil {
				t.Fatalf("create terminal failure chain: %v", err)
			}
			if _, owned, err := requireOutcomeStore(t, store).FailChainNode(ctx, "chain-failure-terminal-dispatch-mismatch", nodeID, errors.New("current failure")); err != nil || !owned {
				t.Fatalf("fail current chain = owned:%t err:%v", owned, err)
			}
			staleFailure, err := requireChainFailureStore(t, store).failChainOutcome(ctx, "chain-failure-terminal-dispatch-mismatch", nodeID, errors.New("stale failure"), mismatches[1])
			if err != nil || staleFailure.owned || staleFailure.claimedNow || staleFailure.receiptKnown {
				t.Fatalf("terminal stale chain failure = %+v err:%v, want pure non-owner no-op", staleFailure, err)
			}

			if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, DispatchID: "dispatch-current-batch", Jobs: []BatchJob{{JobID: jobID}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			for index, mismatch := range mismatches {
				if _, err := requireBatchSettlementStore(t, store).settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, mismatch); err == nil || !strings.Contains(err.Error(), "dispatch mismatch") {
					t.Fatalf("batch mismatch %d error = %v", index, err)
				}
			}
			batch, err := store.GetBatch(ctx, batchID)
			if err != nil || batch.Processed != 0 || batch.Pending != 1 || batch.Completed || batch.Cancelled {
				t.Fatalf("batch after mismatch = %+v err:%v", batch, err)
			}
			if receipt, known, err := requireTransitionReceiptStore(t, store).batchTransitionReceipt(ctx, batchID, jobID); err != nil || known {
				t.Fatalf("batch mismatch receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}
			if state, done, err := store.MarkBatchJobSucceeded(ctx, batchID, jobID); err != nil || !done || !state.Completed {
				t.Fatalf("complete current batch = %+v done:%t err:%v", state, done, err)
			}
			staleBatch, err := requireBatchSettlementStore(t, store).settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, mismatches[1])
			if err != nil || staleBatch.owned || staleBatch.claimedNow || staleBatch.receiptKnown {
				t.Fatalf("terminal stale batch settlement = %+v err:%v, want pure non-owner no-op", staleBatch, err)
			}

			legacyClaim := transitionClaim{deliveryID: "generation-legacy-dispatch", attempt: 0, dispatchID: "dispatch-transport-only", jobID: "job-legacy-dispatch", jobFingerprint: "fingerprint-legacy-dispatch"}
			if err := store.CreateChain(ctx, ChainRecord{ChainID: "chain-legacy-empty-dispatch", Nodes: []ChainNode{{NodeID: "node-legacy-empty-dispatch"}}}); err != nil {
				t.Fatalf("create legacy chain: %v", err)
			}
			legacyChain, err := requireChainAdvanceStore(t, store).advanceChainOutcome(ctx, "chain-legacy-empty-dispatch", "node-legacy-empty-dispatch", legacyClaim)
			if err != nil || !legacyChain.claimedNow || !legacyChain.receiptKnown || legacyChain.receipt.workflowDispatchID != "" || legacyChain.receipt.owner.dispatchID != legacyClaim.dispatchID {
				t.Fatalf("legacy chain claim = %+v err:%v", legacyChain, err)
			}
			if err := store.CreateChain(ctx, ChainRecord{ChainID: "chain-failure-legacy-empty-dispatch", Nodes: []ChainNode{{NodeID: "node-failure-legacy-empty-dispatch"}}}); err != nil {
				t.Fatalf("create legacy failure chain: %v", err)
			}
			legacyFailure, err := requireChainFailureStore(t, store).failChainOutcome(ctx, "chain-failure-legacy-empty-dispatch", "node-failure-legacy-empty-dispatch", errors.New("legacy failure"), legacyClaim)
			if err != nil || !legacyFailure.claimedNow || !legacyFailure.receiptKnown || legacyFailure.receipt.workflowDispatchID != "" || legacyFailure.receipt.owner.dispatchID != legacyClaim.dispatchID {
				t.Fatalf("legacy chain failure claim = %+v err:%v", legacyFailure, err)
			}
			if err := store.CreateBatch(ctx, BatchRecord{BatchID: "batch-legacy-empty-dispatch", Jobs: []BatchJob{{JobID: "job-legacy-empty-dispatch"}}}); err != nil {
				t.Fatalf("create legacy batch: %v", err)
			}
			legacyBatchClaim := legacyClaim
			legacyBatchClaim.jobID = "job-legacy-empty-dispatch"
			legacyBatch, err := requireBatchSettlementStore(t, store).settleBatchOutcome(ctx, "batch-legacy-empty-dispatch", "job-legacy-empty-dispatch", BatchJobSucceeded, nil, legacyBatchClaim)
			if err != nil || !legacyBatch.claimedNow || !legacyBatch.receiptKnown || legacyBatch.receipt.workflowDispatchID != "" || legacyBatch.receipt.owner.dispatchID != legacyClaim.dispatchID {
				t.Fatalf("legacy batch claim = %+v err:%v", legacyBatch, err)
			}
		})
	}
}

// TestStoreContract_FailChainPreservesReceiptBackedCause proves the legacy
// terminal method cannot replace the cause bound to an immutable failed receipt.
func TestStoreContract_FailChainPreservesReceiptBackedCause(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			const (
				chainID    = "chain-receipt-backed-first-cause"
				nodeID     = "node-receipt-backed-first-cause"
				dispatchID = "dispatch-receipt-backed-first-cause"
			)
			if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: []ChainNode{{NodeID: nodeID}}}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			claim := transitionClaim{deliveryID: "generation-receipt-backed-first-cause", attempt: 2, dispatchID: dispatchID, jobID: "job-receipt-backed-first-cause", jobFingerprint: "fingerprint-receipt-backed-first-cause"}
			result, err := requireChainFailureStore(t, store).failChainOutcome(ctx, chainID, nodeID, errors.New("authoritative first cause"), claim)
			if err != nil || !result.claimedNow || !result.receiptKnown {
				t.Fatalf("commit receipt-backed failure = %+v err:%v", result, err)
			}
			if err := store.FailChain(ctx, chainID, errors.New("replacement cause")); err != nil {
				t.Fatalf("repeat legacy failure: %v", err)
			}
			state, err := store.GetChain(ctx, chainID)
			if err != nil || !state.Failed || state.Completed || state.Failure != "authoritative first cause" {
				t.Fatalf("chain after replacement attempt = %+v err:%v", state, err)
			}
			receipt, known, err := requireTransitionReceiptStore(t, store).chainTransitionReceipt(ctx, chainID, nodeID)
			if err != nil || !known || receipt.owner != claim || receipt.outcome != BatchJobFailed {
				t.Fatalf("receipt after replacement attempt = known:%t receipt:%+v err:%v", known, receipt, err)
			}
		})
	}
}

// TestStoreContract_TransitionReceiptIncarnationMismatchFailsClosed proves a
// persisted row for another parent incarnation is never collapsed into absence.
func TestStoreContract_TransitionReceiptIncarnationMismatchFailsClosed(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			const (
				chainID = "chain-receipt-incarnation-mismatch"
				nodeID  = "node-receipt-incarnation-mismatch"
				batchID = "batch-receipt-incarnation-mismatch"
				jobID   = "job-receipt-incarnation-mismatch"
			)
			chainClaim := transitionClaim{deliveryID: "generation-chain-incarnation", attempt: 0, dispatchID: "dispatch-chain-incarnation", jobID: "job-chain-incarnation", jobFingerprint: "fingerprint-chain-incarnation"}
			if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, DispatchID: chainClaim.dispatchID, Nodes: []ChainNode{{NodeID: nodeID}}}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if result, err := requireChainAdvanceStore(t, store).advanceChainOutcome(ctx, chainID, nodeID, chainClaim); err != nil || !result.receiptKnown {
				t.Fatalf("commit chain receipt = %+v err:%v", result, err)
			}
			corruptTransitionReceiptDispatch(t, store, chainTransitionKind, chainID, nodeID)
			if receipt, known, err := requireTransitionReceiptStore(t, store).chainTransitionReceipt(ctx, chainID, nodeID); err == nil || known || !strings.Contains(err.Error(), "incarnation") {
				t.Fatalf("mismatched chain receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}

			batchClaim := transitionClaim{deliveryID: "generation-batch-incarnation", attempt: 0, dispatchID: "dispatch-batch-incarnation", jobID: jobID, jobFingerprint: "fingerprint-batch-incarnation"}
			if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, DispatchID: batchClaim.dispatchID, Jobs: []BatchJob{{JobID: jobID}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			if result, err := requireBatchSettlementStore(t, store).settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, batchClaim); err != nil || !result.receiptKnown {
				t.Fatalf("commit batch receipt = %+v err:%v", result, err)
			}
			corruptTransitionReceiptDispatch(t, store, batchTransitionKind, batchID, jobID)
			if receipt, known, err := requireTransitionReceiptStore(t, store).batchTransitionReceipt(ctx, batchID, jobID); err == nil || known || !strings.Contains(err.Error(), "incarnation") {
				t.Fatalf("mismatched batch receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}
		})
	}
}

// corruptTransitionReceiptDispatch simulates a retained row whose parent
// identity no longer matches without depending on one store's internals in callers.
func corruptTransitionReceiptDispatch(t *testing.T, store Store, kind, workflowID, memberID string) {
	t.Helper()
	switch concrete := store.(type) {
	case *memoryStore:
		concrete.mu.Lock()
		key := transitionReceiptKey{workflowKind: kind, workflowID: workflowID, memberID: memberID}
		receipt := concrete.transitionReceipts[key]
		receipt.workflowDispatchID = "dispatch-corrupt-incarnation"
		concrete.transitionReceipts[key] = receipt
		concrete.mu.Unlock()
	case *sqlStore:
		if _, err := concrete.db.Exec(`UPDATE bus_workflow_transition_receipts SET workflow_dispatch_id=? WHERE workflow_kind=? AND workflow_id=? AND member_id=?`, "dispatch-corrupt-incarnation", kind, workflowID, memberID); err != nil {
			t.Fatalf("corrupt SQL transition receipt: %v", err)
		}
	default:
		t.Fatalf("unsupported built-in store %T", store)
	}
}

// TestStoreContract_ConcurrentTransitionReceiptOwner proves every competing
// generation observes the same immutable owner and aggregate outcome.
func TestStoreContract_ConcurrentTransitionReceiptOwner(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			store := factory(t)
			const deliveries = 16

			if err := store.CreateChain(ctx, ChainRecord{
				ChainID:    "chain-concurrent-receipt-owner",
				DispatchID: "dispatch-concurrent-chain-receipt",
				Nodes:      []ChainNode{{NodeID: "node-concurrent-receipt"}},
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			chainStore := requireChainAdvanceStore(t, store)
			type chainObservation struct {
				result chainAdvanceResult
				err    error
			}
			chainStart := make(chan struct{})
			chainResults := make(chan chainObservation, deliveries)
			var chainWait sync.WaitGroup
			for delivery := range deliveries {
				chainWait.Add(1)
				go func(delivery int) {
					defer chainWait.Done()
					<-chainStart
					claim := transitionClaim{
						deliveryID:     fmt.Sprintf("generation-chain-%02d", delivery),
						attempt:        delivery,
						dispatchID:     "dispatch-concurrent-chain-receipt",
						jobID:          "job-concurrent-chain-receipt",
						jobFingerprint: "fingerprint-concurrent-chain-receipt",
					}
					result, err := chainStore.advanceChainOutcome(ctx, "chain-concurrent-receipt-owner", "node-concurrent-receipt", claim)
					chainResults <- chainObservation{result: result, err: err}
				}(delivery)
			}
			close(chainStart)
			waitStoreContractOperations(t, &chainWait)
			close(chainResults)
			chainOwners := make(map[transitionClaim]struct{})
			chainClaims := 0
			for observation := range chainResults {
				if observation.err != nil {
					t.Fatalf("concurrent chain claim: %v", observation.err)
				}
				if observation.result.claimedNow {
					chainClaims++
				}
				if !observation.result.successOwned || !observation.result.receiptKnown {
					t.Fatalf("concurrent chain result = %+v, want owned receipt", observation.result)
				}
				chainOwners[observation.result.receipt.owner] = struct{}{}
			}
			if chainClaims != 1 || len(chainOwners) != 1 {
				t.Fatalf("chain claims/owners = %d/%d, want 1/1", chainClaims, len(chainOwners))
			}

			if err := store.CreateBatch(ctx, BatchRecord{
				BatchID:     "batch-concurrent-receipt-owner",
				DispatchID:  "dispatch-concurrent-batch-receipt",
				AllowFailed: true,
				Jobs:        []BatchJob{{JobID: "job-concurrent-receipt"}},
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			batchStore := requireBatchSettlementStore(t, store)
			type batchObservation struct {
				result batchSettlementResult
				err    error
			}
			batchStart := make(chan struct{})
			batchResults := make(chan batchObservation, deliveries)
			var batchWait sync.WaitGroup
			for delivery := range deliveries {
				batchWait.Add(1)
				go func(delivery int) {
					defer batchWait.Done()
					<-batchStart
					outcome := BatchJobSucceeded
					if delivery%2 == 0 {
						outcome = BatchJobFailed
					}
					claim := transitionClaim{
						deliveryID:     fmt.Sprintf("generation-batch-%02d", delivery),
						attempt:        delivery,
						dispatchID:     "dispatch-concurrent-batch-receipt",
						jobID:          "job-concurrent-receipt",
						jobFingerprint: "fingerprint-concurrent-batch-receipt",
					}
					result, err := batchStore.settleBatchOutcome(ctx, "batch-concurrent-receipt-owner", "job-concurrent-receipt", outcome, errors.New("raced receipt outcome"), claim)
					batchResults <- batchObservation{result: result, err: err}
				}(delivery)
			}
			close(batchStart)
			waitStoreContractOperations(t, &batchWait)
			close(batchResults)
			batchOwners := make(map[transitionClaim]struct{})
			batchOutcomes := make(map[BatchJobOutcome]struct{})
			batchClaims := 0
			for observation := range batchResults {
				if observation.err != nil {
					t.Fatalf("concurrent batch claim: %v", observation.err)
				}
				if observation.result.claimedNow {
					batchClaims++
				}
				if !observation.result.receiptKnown {
					t.Fatalf("concurrent batch result = %+v, want member receipt", observation.result)
				}
				batchOwners[observation.result.receipt.owner] = struct{}{}
				batchOutcomes[observation.result.receipt.outcome] = struct{}{}
			}
			if batchClaims != 1 || len(batchOwners) != 1 || len(batchOutcomes) != 1 {
				t.Fatalf("batch claims/owners/outcomes = %d/%d/%d, want 1/1/1", batchClaims, len(batchOwners), len(batchOutcomes))
			}
			terminalReceipt, known, err := requireTransitionReceiptStore(t, store).batchTransitionReceipt(ctx, "batch-concurrent-receipt-owner", "job-concurrent-receipt")
			if err != nil || !known || !terminalReceipt.aggregateCompleted {
				t.Fatalf("terminal batch receipt = known:%t receipt:%+v err:%v", known, terminalReceipt, err)
			}
		})
	}
}

// TestStoreContract_ConcurrentDistinctBatchReceiptOwner proves concurrent
// member receipts cannot compete for terminal aggregate ownership after their
// parent updates serialize, including fail-fast completion before pending
// members finish.
func TestStoreContract_ConcurrentDistinctBatchReceiptOwner(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			for _, policy := range []struct {
				name          string
				allowFailures bool
			}{
				{name: "allow_failures", allowFailures: true},
				{name: "fail_fast", allowFailures: false},
			} {
				t.Run(policy.name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					store := factory(t)
					settlements := requireBatchSettlementStore(t, store)
					receipts := requireTransitionReceiptStore(t, store)
					const memberCount = 16
					batchID := "batch-distinct-receipt-" + policy.name
					dispatchID := "dispatch-distinct-receipt-" + policy.name
					jobs := make([]BatchJob, memberCount)
					claims := make(map[string]transitionClaim, memberCount)
					outcomes := make(map[string]BatchJobOutcome, memberCount)
					for member := range memberCount {
						jobID := fmt.Sprintf("job-distinct-receipt-%02d", member)
						jobs[member] = BatchJob{JobID: jobID}
						claims[jobID] = transitionClaim{
							deliveryID:     fmt.Sprintf("generation-distinct-receipt-%02d", member),
							attempt:        member,
							dispatchID:     dispatchID,
							jobID:          "delivery-" + jobID,
							jobFingerprint: "fingerprint-" + jobID,
						}
						outcomes[jobID] = BatchJobSucceeded
						if member%2 == 1 {
							outcomes[jobID] = BatchJobFailed
						}
					}
					if err := store.CreateBatch(ctx, BatchRecord{
						BatchID:     batchID,
						DispatchID:  dispatchID,
						AllowFailed: policy.allowFailures,
						Jobs:        jobs,
					}); err != nil {
						t.Fatalf("create batch: %v", err)
					}

					type settlementObservation struct {
						jobID  string
						result batchSettlementResult
						err    error
					}
					start := make(chan struct{})
					observations := make(chan settlementObservation, memberCount)
					var wg sync.WaitGroup
					for _, job := range jobs {
						wg.Add(1)
						go func(jobID string) {
							defer wg.Done()
							<-start
							outcome := outcomes[jobID]
							result, err := settlements.settleBatchOutcome(ctx, batchID, jobID, outcome, errors.New("concurrent member failure"), claims[jobID])
							observations <- settlementObservation{jobID: jobID, result: result, err: err}
						}(job.JobID)
					}
					close(start)
					waitStoreContractOperations(t, &wg)
					close(observations)

					aggregateOwners := 0
					for observation := range observations {
						if observation.err != nil {
							t.Fatalf("settle member %q: %v", observation.jobID, observation.err)
						}
						wantClaim := claims[observation.jobID]
						wantOutcome := outcomes[observation.jobID]
						if !observation.result.claimedNow || !observation.result.owned || !observation.result.receiptKnown || observation.result.receipt.owner != wantClaim || observation.result.receipt.outcome != wantOutcome {
							t.Fatalf("member %q result = %+v, want its exact receipt owner and outcome", observation.jobID, observation.result)
						}
						if observation.result.receipt.aggregateCompleted {
							aggregateOwners++
						}
					}
					if aggregateOwners != 1 {
						t.Fatalf("aggregate owners returned during settlement = %d, want 1", aggregateOwners)
					}

					persistedOwners := 0
					for _, job := range jobs {
						receipt, known, err := receipts.batchTransitionReceipt(ctx, batchID, job.JobID)
						if err != nil || !known || receipt.owner != claims[job.JobID] || receipt.outcome != outcomes[job.JobID] {
							t.Fatalf("persisted receipt for %q = known:%t receipt:%+v err:%v", job.JobID, known, receipt, err)
						}
						if receipt.aggregateCompleted {
							persistedOwners++
							if receipt.aggregateCancelled != !policy.allowFailures {
								t.Fatalf("terminal receipt for %q cancelled = %t, want %t", job.JobID, receipt.aggregateCancelled, !policy.allowFailures)
							}
						}
					}
					if persistedOwners != 1 {
						t.Fatalf("persisted aggregate receipt owners = %d, want 1", persistedOwners)
					}
					state, err := store.GetBatch(ctx, batchID)
					if err != nil {
						t.Fatalf("get batch: %v", err)
					}
					if state.Pending != 0 || state.Processed != memberCount || state.Failed != memberCount/2 || !state.Completed || state.Cancelled != !policy.allowFailures {
						t.Fatalf("batch state = %+v, want exact counters and cancelled=%t", state, !policy.allowFailures)
					}
				})
			}
		})
	}
}

// TestStoreContract_LegacyTransitionRemainsReceiptUnknown proves state written
// through the established public API is never retroactively assigned an owner.
func TestStoreContract_LegacyTransitionRemainsReceiptUnknown(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			receipts := requireTransitionReceiptStore(t, store)
			if err := store.CreateChain(ctx, ChainRecord{ChainID: "chain-legacy-receipt", DispatchID: "dispatch-legacy-chain", Nodes: []ChainNode{{NodeID: "node-legacy-receipt"}}}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if _, done, err := store.AdvanceChain(ctx, "chain-legacy-receipt", "node-legacy-receipt"); err != nil || !done {
				t.Fatalf("legacy chain advance = done:%t err:%v", done, err)
			}
			if receipt, known, err := receipts.chainTransitionReceipt(ctx, "chain-legacy-receipt", "node-legacy-receipt"); err != nil || known {
				t.Fatalf("legacy chain receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}

			if err := store.CreateBatch(ctx, BatchRecord{BatchID: "batch-legacy-receipt", DispatchID: "dispatch-legacy-batch", Jobs: []BatchJob{{JobID: "job-legacy-receipt"}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			if _, done, err := store.MarkBatchJobSucceeded(ctx, "batch-legacy-receipt", "job-legacy-receipt"); err != nil || !done {
				t.Fatalf("legacy batch settlement = done:%t err:%v", done, err)
			}
			if receipt, known, err := receipts.batchTransitionReceipt(ctx, "batch-legacy-receipt", "job-legacy-receipt"); err != nil || known {
				t.Fatalf("legacy batch receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}
		})
	}
}

// TestStoreContract_RejectsAmbiguousChainRecords protects the immutable order
// required by atomic per-node success and failure compare-and-swap operations.
func TestStoreContract_RejectsAmbiguousChainRecords(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			for _, test := range []struct {
				name   string
				record ChainRecord
			}{
				{name: "empty chain id", record: ChainRecord{Nodes: []ChainNode{{NodeID: "node-0"}}}},
				{name: "no nodes", record: ChainRecord{ChainID: "chain-no-nodes"}},
				{name: "empty node id", record: ChainRecord{ChainID: "chain-empty-node", Nodes: []ChainNode{{}}}},
				{name: "duplicate node id", record: ChainRecord{ChainID: "chain-duplicate-node", Nodes: []ChainNode{{NodeID: "node-shared"}, {NodeID: "node-shared"}}}},
			} {
				t.Run(test.name, func(t *testing.T) {
					if err := store.CreateChain(ctx, test.record); err == nil {
						t.Fatal("ambiguous chain record was accepted")
					}
					if _, err := store.GetChain(ctx, test.record.ChainID); !errors.Is(err, ErrNotFound) {
						t.Fatalf("invalid chain persisted: %v", err)
					}
				})
			}
		})
	}
}

// TestStoreContract_RejectsAmbiguousBatchRecords protects the stable member
// identity required by first-writer outcome arbitration.
func TestStoreContract_RejectsAmbiguousBatchRecords(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			for _, test := range []struct {
				name   string
				record BatchRecord
			}{
				{name: "empty batch id", record: BatchRecord{Jobs: []BatchJob{{JobID: "job-0"}}}},
				{name: "no jobs", record: BatchRecord{BatchID: "batch-no-jobs"}},
				{name: "empty job id", record: BatchRecord{BatchID: "batch-empty-job", Jobs: []BatchJob{{}}}},
				{name: "duplicate job id", record: BatchRecord{BatchID: "batch-duplicate-job", Jobs: []BatchJob{{JobID: "job-shared"}, {JobID: "job-shared"}}}},
			} {
				t.Run(test.name, func(t *testing.T) {
					if err := store.CreateBatch(ctx, test.record); err == nil {
						t.Fatal("ambiguous batch record was accepted")
					}
					if test.record.BatchID == "" {
						return
					}
					if _, err := store.GetBatch(ctx, test.record.BatchID); !errors.Is(err, ErrNotFound) {
						t.Fatalf("invalid batch persisted: %v", err)
					}
				})
			}
		})
	}
}

// TestStoreContract_BatchStartRejectsUnknownMember prevents a malformed
// delivery from creating a synthetic member before outcome settlement.
func TestStoreContract_BatchStartRejectsUnknownMember(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			const batchID = "batch-start-membership"
			if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, Jobs: []BatchJob{{JobID: "job-known"}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			before, err := store.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch before unknown start: %v", err)
			}
			if err := store.MarkBatchJobStarted(ctx, batchID, "job-missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unknown member start error = %v, want ErrNotFound", err)
			}
			after, err := store.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch after unknown start: %v", err)
			}
			if after.Pending != before.Pending || after.Processed != before.Processed || after.Failed != before.Failed || after.Completed != before.Completed || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("unknown member start changed batch: before=%+v after=%+v", before, after)
			}
			if err := store.MarkBatchJobStarted(ctx, batchID, "job-known"); err != nil {
				t.Fatalf("start known member: %v", err)
			}
			if err := store.MarkBatchJobStarted(ctx, batchID, "job-known"); err != nil {
				t.Fatalf("replay known member start: %v", err)
			}
		})
	}
}

// TestStoreContract_ChainRecordOwnership prevents callers from changing the
// node identity or payload that outcome arbitration treats as immutable.
func TestStoreContract_ChainRecordOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			record := ChainRecord{
				ChainID: "chain-record-ownership",
				Nodes: []ChainNode{
					{NodeID: "node-owned", Job: StoredJob{Payload: []byte("owned")}},
					{NodeID: "node-successor", Job: StoredJob{Payload: []byte("successor")}},
				},
			}
			if err := store.CreateChain(ctx, record); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			record.Nodes[0].NodeID = "node-mutated"
			record.Nodes[0].Job.Payload[0] = '!'
			state, err := store.GetChain(ctx, record.ChainID)
			if err != nil {
				t.Fatalf("get chain: %v", err)
			}
			if state.Nodes[0].NodeID != "node-owned" || string(state.Nodes[0].Job.Payload) != "owned" {
				t.Fatalf("creation record aliases state: %+v", state.Nodes[0])
			}
			state.Nodes[0].NodeID = "node-return-mutated"
			state.Nodes[0].Job.Payload[0] = '?'
			state, err = store.GetChain(ctx, record.ChainID)
			if err != nil {
				t.Fatalf("get chain again: %v", err)
			}
			if state.Nodes[0].NodeID != "node-owned" || string(state.Nodes[0].Job.Payload) != "owned" {
				t.Fatalf("returned state aliases store: %+v", state.Nodes[0])
			}
			next, done, err := store.AdvanceChain(ctx, record.ChainID, "node-owned")
			if err != nil || done || next == nil {
				t.Fatalf("advance to successor = next:%+v done:%t err:%v", next, done, err)
			}
			next.NodeID = "node-successor-mutated"
			next.Job.Payload[0] = '!'
			state, err = store.GetChain(ctx, record.ChainID)
			if err != nil {
				t.Fatalf("get chain after successor mutation: %v", err)
			}
			if state.Nodes[1].NodeID != "node-successor" || string(state.Nodes[1].Job.Payload) != "successor" {
				t.Fatalf("returned successor aliases store: %+v", state.Nodes[1])
			}
		})
	}
}

// TestChainNodeDispositionsRejectInvalidPersistedIndex covers corrupt state
// that no valid creation or transition path can produce intentionally.
func TestChainNodeDispositionsRejectInvalidPersistedIndex(t *testing.T) {
	for _, nextIndex := range []int{-1, 1} {
		state := ChainState{ChainID: "chain-invalid-index", Nodes: []ChainNode{{NodeID: "node-0"}}, NextIndex: nextIndex}
		if _, _, _, err := chainNodeAdvanceDisposition(state, "node-0"); err == nil {
			t.Fatalf("advance accepted next index %d", nextIndex)
		}
		if _, _, err := chainNodeFailureDisposition(state, "node-0"); err == nil {
			t.Fatalf("failure accepted next index %d", nextIndex)
		}
	}
}

// TestStoreContract_ChainNodeOutcomeOwnership proves a physical redelivery
// cannot replace the first result committed for a sequential node.
func TestStoreContract_ChainNodeOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()

			t.Run("success first", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const chainID = "chain-success-first"
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				if _, done, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil || done {
					t.Fatalf("advance first node = done:%t err:%v", done, err)
				}
				state, owned, err := outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("late failure"))
				if err != nil || owned {
					t.Fatalf("late failure = owned:%t err:%v", owned, err)
				}
				if state.NextIndex != 1 || state.Completed || state.Failed {
					t.Fatalf("success-first state = %+v", state)
				}
			})

			t.Run("failure first and replay", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const chainID = "chain-failure-first"
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				firstCause := errors.New("first failure")
				state, owned, err := outcomes.FailChainNode(ctx, chainID, "node-0", firstCause)
				if err != nil || !owned || !state.Failed || state.NextIndex != 0 {
					t.Fatalf("first failure = owned:%t state:%+v err:%v", owned, state, err)
				}
				state, owned, err = outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("replacement failure"))
				if err != nil || !owned || state.Failure != firstCause.Error() {
					t.Fatalf("failure replay = owned:%t state:%+v err:%v", owned, state, err)
				}
				if _, done, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil || !done {
					t.Fatalf("late success = done:%t err:%v", done, err)
				}
			})

			t.Run("stale and invalid nodes", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const chainID = "chain-node-validation"
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}, {NodeID: "node-2"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				if _, _, err := outcomes.FailChainNode(ctx, chainID, "node-1", errors.New("future failure")); err == nil {
					t.Fatal("future failure was accepted")
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "node-1"); err == nil {
					t.Fatal("future success was accepted")
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "missing-node"); err == nil {
					t.Fatal("unknown success was accepted")
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil {
					t.Fatalf("advance current node: %v", err)
				}
				laterCause := errors.New("later node failed")
				if _, owned, err := outcomes.FailChainNode(ctx, chainID, "node-1", laterCause); err != nil || !owned {
					t.Fatalf("fail current node = owned:%t err:%v", owned, err)
				}
				state, owned, err := outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("stale failure"))
				if err != nil || owned || !state.Failed || state.Failure != laterCause.Error() || state.NextIndex != 1 {
					t.Fatalf("stale failure = owned:%t state:%+v err:%v", owned, state, err)
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "node-2"); err == nil {
					t.Fatal("future success after failure was accepted")
				}
				if _, _, err := outcomes.FailChainNode(ctx, chainID, "node-2", errors.New("future failure")); err == nil {
					t.Fatal("future failure after failure was accepted")
				}
			})
		})
	}
}

// TestStoreContract_TerminalChainRejectsUnknownNodes keeps malformed
// deliveries from inheriting the idempotent result of a real terminal node.
func TestStoreContract_TerminalChainRejectsUnknownNodes(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			for _, terminal := range []string{"completed", "failed"} {
				t.Run(terminal, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
					defer cancel()
					store := factory(t)
					outcomes := requireOutcomeStore(t, store)
					chainID := "chain-terminal-unknown-" + terminal
					if err := store.CreateChain(ctx, ChainRecord{
						ChainID: chainID,
						Nodes: []ChainNode{
							{NodeID: "node-0"},
							{NodeID: "node-1"},
						},
					}); err != nil {
						t.Fatalf("create chain: %v", err)
					}
					if _, _, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil {
						t.Fatalf("advance first node: %v", err)
					}
					if terminal == "completed" {
						if _, done, err := store.AdvanceChain(ctx, chainID, "node-1"); err != nil || !done {
							t.Fatalf("complete chain = done:%t err:%v", done, err)
						}
					} else {
						if _, owned, err := outcomes.FailChainNode(ctx, chainID, "node-1", errors.New("terminal failure")); err != nil || !owned {
							t.Fatalf("fail chain = owned:%t err:%v", owned, err)
						}
					}
					before, err := store.GetChain(ctx, chainID)
					if err != nil {
						t.Fatalf("get terminal chain: %v", err)
					}
					if _, _, err := store.AdvanceChain(ctx, chainID, "node-missing"); err == nil {
						t.Fatal("unknown success inherited terminal state")
					}
					if _, _, err := outcomes.FailChainNode(ctx, chainID, "node-missing", errors.New("unknown failure")); err == nil {
						t.Fatal("unknown failure inherited terminal state")
					}
					after, err := store.GetChain(ctx, chainID)
					if err != nil {
						t.Fatalf("get chain after unknown deliveries: %v", err)
					}
					if after.NextIndex != before.NextIndex || after.Completed != before.Completed || after.Failed != before.Failed || after.Failure != before.Failure || !after.UpdatedAt.Equal(before.UpdatedAt) {
						t.Fatalf("unknown delivery changed terminal chain: before=%+v after=%+v", before, after)
					}
				})
			}
		})
	}
}

// TestStoreContract_ConcurrentChainNodeOutcomeOwnership repeatedly races both
// outcomes and accepts only one of the two valid linearized states.
func TestStoreContract_ConcurrentChainNodeOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for iteration := range 12 {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				chainID := fmt.Sprintf("chain-outcome-race-%02d", iteration)
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				start := make(chan struct{})
				errs := make(chan error, 32)
				var wg sync.WaitGroup
				for delivery := range 32 {
					wg.Add(1)
					go func(fail bool) {
						defer wg.Done()
						<-start
						if fail {
							_, _, err := outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("raced failure"))
							errs <- err
							return
						}
						_, _, err := store.AdvanceChain(ctx, chainID, "node-0")
						errs <- err
					}(delivery%2 == 0)
				}
				close(start)
				waitStoreContractOperations(t, &wg)
				close(errs)
				for err := range errs {
					if err != nil {
						t.Fatalf("race operation: %v", err)
					}
				}
				state, err := store.GetChain(ctx, chainID)
				if err != nil {
					t.Fatalf("get raced chain: %v", err)
				}
				successWon := state.NextIndex == 1 && !state.Failed && !state.Completed
				failureWon := state.NextIndex == 0 && state.Failed && !state.Completed
				if !successWon && !failureWon {
					t.Fatalf("non-linearized raced state = %+v", state)
				}
			}
		})
	}
}

// TestStoreContract_ConcurrentFinalChainNodeOutcomeOwnership races failure
// against the two-step SQL advancement that also marks the chain completed.
func TestStoreContract_ConcurrentFinalChainNodeOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for iteration := range 8 {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				chainID := fmt.Sprintf("chain-final-outcome-race-%02d", iteration)
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-final"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				start := make(chan struct{})
				errs := make(chan error, 32)
				var wg sync.WaitGroup
				for delivery := range 32 {
					wg.Add(1)
					go func(fail bool) {
						defer wg.Done()
						<-start
						if fail {
							_, _, err := outcomes.FailChainNode(ctx, chainID, "node-final", errors.New("raced final failure"))
							errs <- err
							return
						}
						_, _, err := store.AdvanceChain(ctx, chainID, "node-final")
						errs <- err
					}(delivery%2 == 0)
				}
				close(start)
				waitStoreContractOperations(t, &wg)
				close(errs)
				for err := range errs {
					if err != nil {
						t.Fatalf("race final chain outcome: %v", err)
					}
				}
				state, err := store.GetChain(ctx, chainID)
				if err != nil {
					t.Fatalf("get raced final chain: %v", err)
				}
				successWon := state.NextIndex == 1 && state.Completed && !state.Failed
				failureWon := state.NextIndex == 0 && !state.Completed && state.Failed
				if !successWon && !failureWon {
					t.Fatalf("non-linearized final chain state = %+v", state)
				}
			}
		})
	}
}

// TestStoreContract_BatchJobOutcomeOwnership proves contradictory redelivery
// cannot change either member state or the aggregate's logical winner.
func TestStoreContract_BatchJobOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			for _, first := range []BatchJobOutcome{BatchJobSucceeded, BatchJobFailed} {
				t.Run(string(first), func(t *testing.T) {
					store := factory(t)
					outcomes := requireOutcomeStore(t, store)
					batchID := "batch-outcome-" + string(first)
					if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, AllowFailed: true, Jobs: []BatchJob{{JobID: "job-0"}, {JobID: "job-1"}}}); err != nil {
						t.Fatalf("create batch: %v", err)
					}
					state, owned, err := outcomes.SettleBatchJob(ctx, batchID, "job-0", first, errors.New("first cause"))
					if err != nil || !owned {
						t.Fatalf("first outcome = owned:%t err:%v", owned, err)
					}
					if state.Pending != 1 || state.Processed != 1 || state.Failed != boolInt(first == BatchJobFailed) {
						t.Fatalf("first outcome state = %+v", state)
					}
					if _, owned, err := outcomes.SettleBatchJob(ctx, batchID, "job-0", first, nil); err != nil || !owned {
						t.Fatalf("same-outcome replay = owned:%t err:%v", owned, err)
					}
					opposite := BatchJobFailed
					if first == BatchJobFailed {
						opposite = BatchJobSucceeded
					}
					state, owned, err = outcomes.SettleBatchJob(ctx, batchID, "job-0", opposite, errors.New("opposite cause"))
					if err != nil || owned || state.Pending != 1 || state.Processed != 1 || state.Failed != boolInt(first == BatchJobFailed) {
						t.Fatalf("opposite replay = owned:%t state:%+v err:%v", owned, state, err)
					}
				})
			}
			t.Run("invalid outcome", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const batchID = "batch-invalid-outcome"
				if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, Jobs: []BatchJob{{JobID: "job-0"}}}); err != nil {
					t.Fatalf("create batch: %v", err)
				}
				if _, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-0", BatchJobOutcome("unknown"), nil); err == nil {
					t.Fatal("invalid batch outcome was accepted")
				}
				state, err := store.GetBatch(ctx, batchID)
				if err != nil {
					t.Fatalf("get batch: %v", err)
				}
				if state.Pending != 1 || state.Processed != 0 || state.Failed != 0 || state.Completed {
					t.Fatalf("invalid outcome changed state: %+v", state)
				}
			})
			t.Run("missing member", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const batchID = "batch-missing-member"
				if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, Jobs: []BatchJob{{JobID: "job-known"}}}); err != nil {
					t.Fatalf("create batch: %v", err)
				}
				if _, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-missing", BatchJobSucceeded, nil); !errors.Is(err, ErrNotFound) {
					t.Fatalf("missing member outcome error = %v, want ErrNotFound", err)
				}
				state, err := store.GetBatch(ctx, batchID)
				if err != nil {
					t.Fatalf("get batch: %v", err)
				}
				if state.Pending != 1 || state.Processed != 0 || state.Failed != 0 || state.Completed {
					t.Fatalf("missing member outcome changed state: %+v", state)
				}
			})
		})
	}
}

// TestStoreContract_ConcurrentBatchJobOutcomeOwnership makes every delivery
// observe one immutable member winner while aggregate counters advance once.
func TestStoreContract_ConcurrentBatchJobOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			outcomes := requireOutcomeStore(t, store)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const batchID = "batch-concurrent-outcome"
			if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, AllowFailed: true, Jobs: []BatchJob{{JobID: "job-shared"}, {JobID: "job-pending"}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			start := make(chan struct{})
			errs := make(chan error, 32)
			var wg sync.WaitGroup
			for delivery := range 32 {
				outcome := BatchJobSucceeded
				if delivery%2 == 0 {
					outcome = BatchJobFailed
				}
				wg.Add(1)
				go func(outcome BatchJobOutcome) {
					defer wg.Done()
					<-start
					_, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-shared", outcome, errors.New("raced outcome"))
					errs <- err
				}(outcome)
			}
			close(start)
			waitStoreContractOperations(t, &wg)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent settlement: %v", err)
				}
			}
			state, err := store.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch: %v", err)
			}
			if state.Pending != 1 || state.Processed != 1 || (state.Failed != 0 && state.Failed != 1) || state.Completed {
				t.Fatalf("concurrent outcome state = %+v", state)
			}
			_, successOwned, err := outcomes.SettleBatchJob(ctx, batchID, "job-shared", BatchJobSucceeded, nil)
			if err != nil {
				t.Fatalf("replay success: %v", err)
			}
			_, failureOwned, err := outcomes.SettleBatchJob(ctx, batchID, "job-shared", BatchJobFailed, errors.New("replayed failure"))
			if err != nil {
				t.Fatalf("replay failure: %v", err)
			}
			if successOwned == failureOwned || successOwned != (state.Failed == 0) {
				t.Fatalf("replay ownership = success:%t failure:%t state:%+v", successOwned, failureOwned, state)
			}
		})
	}
}

// TestStoreContract_ConcurrentTerminalBatchJobOutcomeOwnership races both
// categories through final completion and fail-fast cancellation branches.
func TestStoreContract_ConcurrentTerminalBatchJobOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, policy := range []struct {
				name          string
				allowFailures bool
			}{
				{name: "allow_failures", allowFailures: true},
				{name: "fail_fast", allowFailures: false},
			} {
				t.Run(policy.name, func(t *testing.T) {
					for iteration := range 6 {
						store := factory(t)
						outcomes := requireOutcomeStore(t, store)
						batchID := fmt.Sprintf("batch-terminal-outcome-%s-%02d", policy.name, iteration)
						if err := store.CreateBatch(ctx, BatchRecord{
							BatchID:     batchID,
							AllowFailed: policy.allowFailures,
							Jobs:        []BatchJob{{JobID: "job-final"}},
						}); err != nil {
							t.Fatalf("create batch: %v", err)
						}
						start := make(chan struct{})
						errs := make(chan error, 32)
						var wg sync.WaitGroup
						for delivery := range 32 {
							outcome := BatchJobSucceeded
							if delivery%2 == 0 {
								outcome = BatchJobFailed
							}
							wg.Add(1)
							go func(outcome BatchJobOutcome) {
								defer wg.Done()
								<-start
								_, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-final", outcome, errors.New("raced terminal outcome"))
								errs <- err
							}(outcome)
						}
						close(start)
						waitStoreContractOperations(t, &wg)
						close(errs)
						for err := range errs {
							if err != nil {
								t.Fatalf("race terminal batch outcome: %v", err)
							}
						}
						state, err := store.GetBatch(ctx, batchID)
						if err != nil {
							t.Fatalf("get terminal batch: %v", err)
						}
						if state.Pending != 0 || state.Processed != 1 || !state.Completed || (state.Failed != 0 && state.Failed != 1) {
							t.Fatalf("terminal batch state = %+v", state)
						}
						wantCancelled := state.Failed == 1 && !policy.allowFailures
						if state.Cancelled != wantCancelled {
							t.Fatalf("terminal batch cancellation = %t, want %t for state %+v", state.Cancelled, wantCancelled, state)
						}
					}
				})
			}
		})
	}
}

// boolInt keeps aggregate expectations readable without obscuring the outcome
// condition inside test tables.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// TestStoreContract_ConcurrentDuplicateBatchSettlement proves redelivery can
// claim one member only once even when every delivery observes it concurrently.
func TestStoreContract_ConcurrentDuplicateBatchSettlement(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const batchID = "batch-concurrent-duplicate"
			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "dispatch-concurrent-duplicate",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "job-shared", Job: StoredJob{Type: "reports:shared"}},
					{JobID: "job-final", Job: StoredJob{Type: "reports:final"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			const deliveries = 32
			start := make(chan struct{})
			errs := make(chan error, deliveries)
			var wg sync.WaitGroup
			for range deliveries {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, _, err := s.MarkBatchJobSucceeded(ctx, batchID, "job-shared")
					errs <- err
				}()
			}
			close(start)
			waitStoreContractOperations(t, &wg)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent duplicate settlement: %v", err)
				}
			}

			state, err := s.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch: %v", err)
			}
			if state.Pending != 1 || state.Processed != 1 || state.Failed != 0 || state.Completed {
				t.Fatalf("duplicate settlement state = %+v, want one processed and one pending", state)
			}
		})
	}
}

// TestStoreContract_ConcurrentDistinctBatchSettlement proves aggregate
// counters cannot overwrite one another when independent members finish.
func TestStoreContract_ConcurrentDistinctBatchSettlement(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const jobCount = 32
			jobs := make([]BatchJob, jobCount)
			for i := range jobs {
				jobs[i] = BatchJob{
					JobID: fmt.Sprintf("job-%02d", i),
					Job:   StoredJob{Type: "reports:member"},
				}
			}
			for _, policy := range []struct {
				name          string
				allowFailures bool
			}{
				{name: "allow_failures", allowFailures: true},
				{name: "fail_fast", allowFailures: false},
			} {
				t.Run(policy.name, func(t *testing.T) {
					batchID := "batch-concurrent-distinct-" + policy.name
					if err := s.CreateBatch(ctx, BatchRecord{
						BatchID:     batchID,
						DispatchID:  "dispatch-concurrent-distinct-" + policy.name,
						AllowFailed: policy.allowFailures,
						Jobs:        jobs,
						CreatedAt:   time.Now(),
					}); err != nil {
						t.Fatalf("create batch: %v", err)
					}

					start := make(chan struct{})
					errs := make(chan error, jobCount)
					var wg sync.WaitGroup
					for i, job := range jobs {
						wg.Add(1)
						go func(index int, member BatchJob) {
							defer wg.Done()
							<-start
							var err error
							if index%2 == 0 {
								_, _, err = s.MarkBatchJobSucceeded(ctx, batchID, member.JobID)
							} else {
								_, _, err = s.MarkBatchJobFailed(ctx, batchID, member.JobID, errors.New("member failed"))
							}
							errs <- err
						}(i, job)
					}
					close(start)
					waitStoreContractOperations(t, &wg)
					close(errs)
					for err := range errs {
						if err != nil {
							t.Fatalf("concurrent distinct settlement: %v", err)
						}
					}

					state, err := s.GetBatch(ctx, batchID)
					if err != nil {
						t.Fatalf("get batch: %v", err)
					}
					wantCancelled := !policy.allowFailures
					if state.Pending != 0 || state.Processed != jobCount || state.Failed != jobCount/2 || !state.Completed || state.Cancelled != wantCancelled {
						t.Fatalf("concurrent settlement state = %+v, want exact aggregate counters and cancelled=%t", state, wantCancelled)
					}
				})
			}
		})
	}
}

// TestStoreContract_DuplicateSuccessCannotBecomeFailure keeps the first
// committed member outcome authoritative across inconsistent redelivery.
func TestStoreContract_DuplicateSuccessCannotBecomeFailure(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const batchID = "batch-immutable-outcome"
			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "dispatch-immutable-outcome",
				AllowFailed: false,
				Jobs: []BatchJob{
					{JobID: "job-first", Job: StoredJob{Type: "reports:first"}},
					{JobID: "job-second", Job: StoredJob{Type: "reports:second"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			if _, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "job-first"); err != nil || done {
				t.Fatalf("mark first success = done:%t err:%v, want active batch", done, err)
			}

			state, done, err := s.MarkBatchJobFailed(ctx, batchID, "job-first", errors.New("inconsistent duplicate"))
			if err != nil {
				t.Fatalf("mark inconsistent duplicate: %v", err)
			}
			if done || state.Pending != 1 || state.Processed != 1 || state.Failed != 0 || state.Cancelled || state.Completed {
				t.Fatalf("inconsistent duplicate state = %+v done:%t, want original success retained", state, done)
			}
		})
	}
}

// TestStoreContract_ConcurrentDuplicateChainAdvance proves every redelivery
// observes the same current successor after one node claim wins.
func TestStoreContract_ConcurrentDuplicateChainAdvance(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const chainID = "chain-concurrent-duplicate"
			if err := s.CreateChain(ctx, ChainRecord{
				ChainID:    chainID,
				DispatchID: "dispatch-concurrent-chain",
				Nodes: []ChainNode{
					{NodeID: "node-first", Job: StoredJob{Type: "reports:first"}},
					{NodeID: "node-second", Job: StoredJob{Type: "reports:second"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}

			const deliveries = 32
			start := make(chan struct{})
			errs := make(chan error, deliveries)
			var wg sync.WaitGroup
			for range deliveries {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					next, done, err := s.AdvanceChain(ctx, chainID, "node-first")
					if err == nil && (done || next == nil || next.NodeID != "node-second") {
						err = fmt.Errorf("next = %+v done:%t, want node-second", next, done)
					}
					errs <- err
				}()
			}
			close(start)
			waitStoreContractOperations(t, &wg)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent duplicate advance: %v", err)
				}
			}

			state, err := s.GetChain(ctx, chainID)
			if err != nil {
				t.Fatalf("get chain: %v", err)
			}
			if state.NextIndex != 1 || state.Completed || state.Failed {
				t.Fatalf("concurrent chain state = %+v, want one committed node", state)
			}
		})
	}
}

func TestStoreContract_NotFound(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()

			if _, err := s.GetChain(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected chain ErrNotFound, got %v", err)
			}
			if _, err := s.GetBatch(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected batch ErrNotFound, got %v", err)
			}
		})
	}
}

func TestStoreContract_ChainAdvanceIdempotent(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			chainID := "chain-contract"

			if err := s.CreateChain(ctx, ChainRecord{
				ChainID:    chainID,
				DispatchID: "d1",
				Queue:      "default",
				Nodes: []ChainNode{
					{NodeID: "n1", Job: StoredJob{Type: "monitor:poll"}},
					{NodeID: "n2", Job: StoredJob{Type: "monitor:downsample"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}

			next, done, err := s.AdvanceChain(ctx, chainID, "n1")
			if err != nil {
				t.Fatalf("advance first: %v", err)
			}
			if done || next == nil || next.NodeID != "n2" {
				t.Fatalf("expected next n2 on first advance, done=%v next=%+v", done, next)
			}

			next, done, err = s.AdvanceChain(ctx, chainID, "n1")
			if err != nil {
				t.Fatalf("advance duplicate: %v", err)
			}
			if done || next == nil || next.NodeID != "n2" {
				t.Fatalf("expected idempotent duplicate advance, done=%v next=%+v", done, next)
			}

			next, done, err = s.AdvanceChain(ctx, chainID, "n2")
			if err != nil {
				t.Fatalf("advance final: %v", err)
			}
			if !done || next != nil {
				t.Fatalf("expected chain done with nil next, done=%v next=%+v", done, next)
			}
		})
	}
}

// TestStoreContract_CompletedChainRejectsLateFailure keeps the first terminal
// outcome authoritative when a competing delivery reports failure too late.
func TestStoreContract_CompletedChainRejectsLateFailure(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			const chainID = "chain-completed-before-failure"
			if err := store.CreateChain(ctx, ChainRecord{
				ChainID:    chainID,
				DispatchID: "dispatch-completed-before-failure",
				Nodes:      []ChainNode{{NodeID: "node-only", Job: StoredJob{Type: "reports:only"}}},
				CreatedAt:  time.Now(),
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if next, done, err := store.AdvanceChain(ctx, chainID, "node-only"); err != nil || !done || next != nil {
				t.Fatalf("complete chain = next:%+v done:%t err:%v", next, done, err)
			}
			if err := store.FailChain(ctx, chainID, errors.New("late competing failure")); err != nil {
				t.Fatalf("fail completed chain: %v", err)
			}
			state, err := store.GetChain(ctx, chainID)
			if err != nil {
				t.Fatalf("get chain: %v", err)
			}
			if !state.Completed || state.Failed || state.Failure != "" {
				t.Fatalf("late failure changed completed chain: %+v", state)
			}
		})
	}
}

func TestStoreContract_BatchTerminalBehavior(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "contract",
				Queue:       "default",
				AllowFailed: false,
				Jobs: []BatchJob{
					{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}},
					{JobID: "j2", Job: StoredJob{Type: "monitor:downsample"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success: %v", err)
			}
			if done {
				t.Fatal("expected batch not done after first success")
			}
			if st.Pending != 1 || st.Processed != 1 || st.Failed != 0 {
				t.Fatalf("unexpected mid state: %+v", st)
			}

			st, done, err = s.MarkBatchJobFailed(ctx, batchID, "j2", errors.New("boom"))
			if err != nil {
				t.Fatalf("mark failed: %v", err)
			}
			if !done {
				t.Fatal("expected batch done on failure when allow_failed=false")
			}
			if !st.Completed || !st.Cancelled || st.Failed != 1 {
				t.Fatalf("unexpected terminal state: %+v", st)
			}
		})
	}
}

func TestStoreContract_CallbackMarkerIdempotent(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			key := "batch_finally:contract"

			first, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("first callback marker: %v", err)
			}
			if !first {
				t.Fatal("expected first callback marker=true")
			}

			second, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("second callback marker: %v", err)
			}
			if second {
				t.Fatal("expected second callback marker=false")
			}
		})
	}
}

func TestStoreContract_PruneClearsOldCallbackMarkers(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			key := "batch_finally:contract-prune"

			first, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("first callback marker: %v", err)
			}
			if !first {
				t.Fatal("expected first callback marker=true")
			}

			// Future cutoff ensures just-inserted marker is considered old.
			if err := s.Prune(ctx, time.Now().Add(1*time.Minute)); err != nil {
				t.Fatalf("prune markers: %v", err)
			}

			again, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("callback marker after prune: %v", err)
			}
			if !again {
				t.Fatal("expected callback marker to be insertable again after prune")
			}
		})
	}
}

func TestStoreContract_BatchAllowFailuresContinues(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-allow-fail-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "allow-fail",
				Queue:       "default",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}},
					{JobID: "j2", Job: StoredJob{Type: "monitor:downsample"}},
					{JobID: "j3", Job: StoredJob{Type: "monitor:alert"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobFailed(ctx, batchID, "j1", errors.New("boom"))
			if err != nil {
				t.Fatalf("mark first failed: %v", err)
			}
			if done {
				t.Fatal("expected batch to continue when allow_failed=true")
			}
			if st.Cancelled {
				t.Fatal("expected batch not cancelled when allow_failed=true")
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j2")
			if err != nil {
				t.Fatalf("mark second success: %v", err)
			}
			if done {
				t.Fatal("expected batch still not done after second job")
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j3")
			if err != nil {
				t.Fatalf("mark third success: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected batch completed, done=%v state=%+v", done, st)
			}
			if st.Failed != 1 || st.Processed != 3 || st.Pending != 0 {
				t.Fatalf("unexpected final counters: %+v", st)
			}
		})
	}
}

func TestStoreContract_BatchDuplicateTerminalUpdateDoesNotDoubleCount(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-dup-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "dup",
				Queue:       "default",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success first: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected completed after first success, done=%v state=%+v", done, st)
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success duplicate: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected completed after duplicate success, done=%v state=%+v", done, st)
			}
			if st.Processed != 1 || st.Pending != 0 || st.Failed != 0 {
				t.Fatalf("expected counters unchanged after duplicate terminal update, got %+v", st)
			}
		})
	}
}
