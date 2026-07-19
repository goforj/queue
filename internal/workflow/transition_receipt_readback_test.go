package workflow

import (
	"context"
	"errors"
	"testing"
)

// TestTransitionReceiptAmbiguousCommitReadback proves a durable receipt can
// resolve an uncertain commit response only for the generation and outcome it
// names.
func TestTransitionReceiptAmbiguousCommitReadback(t *testing.T) {
	t.Run("chain", func(t *testing.T) {
		store := newSQLiteStore(t).(*sqlStore)
		ctx := context.Background()
		const (
			chainID = "chain-ambiguous-commit-readback"
			nodeID  = "node-ambiguous-commit-readback"
		)
		claim := transitionClaim{
			deliveryID:     "generation-chain-ambiguous-commit",
			attempt:        3,
			dispatchID:     "dispatch-chain-ambiguous-commit",
			jobID:          "job-chain-ambiguous-commit",
			jobFingerprint: "fingerprint-chain-ambiguous-commit",
		}
		if err := store.CreateChain(ctx, ChainRecord{
			ChainID:    chainID,
			DispatchID: claim.dispatchID,
			Nodes:      []ChainNode{{NodeID: nodeID}},
		}); err != nil {
			t.Fatalf("create chain: %v", err)
		}
		advanced, err := store.advanceChainOutcome(ctx, chainID, nodeID, claim)
		if err != nil || !advanced.claimedNow || !advanced.done || !advanced.receiptKnown {
			t.Fatalf("commit chain transition = %+v, err:%v", advanced, err)
		}

		commitErr := errors.New("connection lost after chain commit")
		readback, err := store.readCommittedChainAdvance(ctx, chainID, nodeID, claim, commitErr)
		if err != nil || !readback.claimedNow || !readback.successOwned || !readback.done || !readback.receiptKnown || readback.receipt.owner != claim {
			t.Fatalf("chain commit readback = %+v, err:%v", readback, err)
		}
		other := claim
		other.deliveryID = "generation-chain-other"
		if _, err := store.readCommittedChainAdvance(ctx, chainID, nodeID, other, commitErr); !errors.Is(err, commitErr) {
			t.Fatalf("different chain owner readback error = %v, want %v", err, commitErr)
		}
	})

	t.Run("chain failure", func(t *testing.T) {
		store := newSQLiteStore(t).(*sqlStore)
		ctx := context.Background()
		const (
			chainID = "chain-failure-ambiguous-commit-readback"
			nodeID  = "node-failure-ambiguous-commit-readback"
		)
		claim := transitionClaim{
			deliveryID:     "generation-chain-failure-ambiguous-commit",
			attempt:        2,
			dispatchID:     "dispatch-chain-failure-ambiguous-commit",
			jobID:          "job-chain-failure-ambiguous-commit",
			jobFingerprint: "fingerprint-chain-failure-ambiguous-commit",
		}
		if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, DispatchID: claim.dispatchID, Nodes: []ChainNode{{NodeID: nodeID}}}); err != nil {
			t.Fatalf("create chain: %v", err)
		}
		failed, err := store.failChainOutcome(ctx, chainID, nodeID, errors.New("committed chain failure"), claim)
		if err != nil || !failed.claimedNow || !failed.owned || !failed.receiptKnown || failed.receipt.outcome != BatchJobFailed {
			t.Fatalf("commit chain failure = %+v err:%v", failed, err)
		}

		commitErr := errors.New("connection lost after chain failure commit")
		readback, err := store.readCommittedChainFailure(ctx, chainID, nodeID, claim, commitErr)
		if err != nil || !readback.claimedNow || !readback.owned || !readback.receiptKnown || readback.receipt.owner != claim || readback.receipt.outcome != BatchJobFailed || !readback.state.Failed || readback.state.Completed {
			t.Fatalf("chain failure readback = %+v err:%v", readback, err)
		}
		other := claim
		other.deliveryID = "generation-chain-failure-other"
		if _, err := store.readCommittedChainFailure(ctx, chainID, nodeID, other, commitErr); !errors.Is(err, commitErr) {
			t.Fatalf("different chain failure owner readback error = %v, want %v", err, commitErr)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE bus_workflow_transition_receipts SET outcome='succeeded' WHERE workflow_kind=? AND workflow_id=? AND member_id=?`, chainTransitionKind, chainID, nodeID); err != nil {
			t.Fatalf("change chain failure receipt outcome: %v", err)
		}
		if _, err := store.readCommittedChainFailure(ctx, chainID, nodeID, claim, commitErr); !errors.Is(err, commitErr) {
			t.Fatalf("different chain failure outcome readback error = %v, want %v", err, commitErr)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE bus_workflow_transition_receipts SET outcome='failed', aggregate_completed=1 WHERE workflow_kind=? AND workflow_id=? AND member_id=?`, chainTransitionKind, chainID, nodeID); err != nil {
			t.Fatalf("change chain failure receipt completion: %v", err)
		}
		if _, err := store.readCommittedChainFailure(ctx, chainID, nodeID, claim, commitErr); !errors.Is(err, commitErr) {
			t.Fatalf("completed chain failure receipt readback error = %v, want %v", err, commitErr)
		}
	})

	t.Run("batch", func(t *testing.T) {
		store := newSQLiteStore(t).(*sqlStore)
		ctx := context.Background()
		const (
			batchID = "batch-ambiguous-commit-readback"
			jobID   = "job-ambiguous-commit-readback"
		)
		claim := transitionClaim{
			deliveryID:     "generation-batch-ambiguous-commit",
			attempt:        4,
			dispatchID:     "dispatch-batch-ambiguous-commit",
			jobID:          jobID,
			jobFingerprint: "fingerprint-batch-ambiguous-commit",
		}
		if err := store.CreateBatch(ctx, BatchRecord{
			BatchID:     batchID,
			DispatchID:  claim.dispatchID,
			AllowFailed: true,
			Jobs:        []BatchJob{{JobID: jobID}},
		}); err != nil {
			t.Fatalf("create batch: %v", err)
		}
		settled, err := store.settleBatchOutcome(ctx, batchID, jobID, BatchJobFailed, errors.New("member failed"), claim)
		if err != nil || !settled.claimedNow || !settled.owned || !settled.state.Completed || !settled.receiptKnown {
			t.Fatalf("commit batch transition = %+v, err:%v", settled, err)
		}

		commitErr := errors.New("connection lost after batch commit")
		state, done, owned, claimed, receipt, known, err := store.readCommittedBatchSettlement(ctx, batchID, jobID, true, claim, commitErr)
		if err != nil || !done || !owned || !claimed || !known || !state.Completed || !receipt.aggregateCompleted || receipt.owner != claim || receipt.outcome != BatchJobFailed {
			t.Fatalf("batch commit readback = state:%+v done:%t owned:%t claimed:%t receipt:%+v known:%t err:%v", state, done, owned, claimed, receipt, known, err)
		}
		if _, _, _, _, _, _, err := store.readCommittedBatchSettlement(ctx, batchID, jobID, false, claim, commitErr); !errors.Is(err, commitErr) {
			t.Fatalf("different batch outcome readback error = %v, want %v", err, commitErr)
		}
	})

	t.Run("nonterminal batch member after later completion", func(t *testing.T) {
		store := newSQLiteStore(t).(*sqlStore)
		ctx := context.Background()
		const (
			batchID    = "batch-ambiguous-nonterminal-readback"
			dispatchID = "dispatch-ambiguous-nonterminal-readback"
			firstJob   = "job-ambiguous-first"
			finalJob   = "job-ambiguous-final"
		)
		firstClaim := transitionClaim{deliveryID: "generation-ambiguous-first", attempt: 1, dispatchID: dispatchID, jobID: firstJob, jobFingerprint: "fingerprint-ambiguous-first"}
		finalClaim := transitionClaim{deliveryID: "generation-ambiguous-final", attempt: 2, dispatchID: dispatchID, jobID: finalJob, jobFingerprint: "fingerprint-ambiguous-final"}
		if err := store.CreateBatch(ctx, BatchRecord{
			BatchID:     batchID,
			DispatchID:  dispatchID,
			AllowFailed: true,
			Jobs:        []BatchJob{{JobID: firstJob}, {JobID: finalJob}},
		}); err != nil {
			t.Fatalf("create batch: %v", err)
		}
		first, err := store.settleBatchOutcome(ctx, batchID, firstJob, BatchJobSucceeded, nil, firstClaim)
		if err != nil || !first.claimedNow || first.state.Completed || !first.receiptKnown || first.receipt.aggregateCompleted {
			t.Fatalf("settle first member = %+v, err:%v", first, err)
		}
		final, err := store.settleBatchOutcome(ctx, batchID, finalJob, BatchJobSucceeded, nil, finalClaim)
		if err != nil || !final.claimedNow || !final.state.Completed || !final.receiptKnown || !final.receipt.aggregateCompleted {
			t.Fatalf("settle final member = %+v, err:%v", final, err)
		}

		commitErr := errors.New("connection lost after first member commit")
		state, done, owned, claimed, receipt, known, err := store.readCommittedBatchSettlement(ctx, batchID, firstJob, false, firstClaim, commitErr)
		if err != nil || !done || !owned || !claimed || !known || !state.Completed || receipt.aggregateCompleted || receipt.owner != firstClaim {
			t.Fatalf("first member readback = state:%+v done:%t owned:%t claimed:%t receipt:%+v known:%t err:%v", state, done, owned, claimed, receipt, known, err)
		}
		settled := batchSettlementResult{state: state, owned: owned, claimedNow: claimed, receipt: receipt, receiptKnown: known}
		if batchSettlementOwnsTerminal(settled, BatchJobSucceeded) {
			t.Fatal("nonterminal member was credited with completion committed by a later member")
		}
		if !batchSettlementOwnsTerminal(final, BatchJobSucceeded) {
			t.Fatal("exact terminal member receipt did not retain completion ownership")
		}
		mismatched := final
		mismatched.receipt.aggregateCancelled = true
		if batchSettlementOwnsTerminal(mismatched, BatchJobSucceeded) {
			t.Fatal("receipt with contradictory cancellation was accepted as terminal owner")
		}
	})
}

// TestTransitionReceiptUnknownVersionsFailClosed ensures a mixed-version
// worker distinguishes unreadable provenance from an absent receipt.
func TestTransitionReceiptUnknownVersionsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		column string
	}{
		{name: "receipt version", column: "receipt_version"},
		{name: "event schema", column: "event_schema_version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newSQLiteStore(t).(*sqlStore)
			ctx := context.Background()
			claim := transitionClaim{
				deliveryID:     "generation-unknown-version",
				attempt:        0,
				dispatchID:     "dispatch-unknown-version",
				jobID:          "job-unknown-version",
				jobFingerprint: "fingerprint-unknown-version",
			}
			if err := store.CreateChain(ctx, ChainRecord{ChainID: "chain-unknown-version", DispatchID: claim.dispatchID, Nodes: []ChainNode{{NodeID: "node-unknown-version"}}}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if result, err := store.advanceChainOutcome(ctx, "chain-unknown-version", "node-unknown-version", claim); err != nil || !result.receiptKnown {
				t.Fatalf("advance chain = %+v, err:%v", result, err)
			}
			query := "UPDATE bus_workflow_transition_receipts SET " + test.column + "=? WHERE workflow_kind=? AND workflow_id=? AND member_id=?"
			if _, err := store.db.ExecContext(ctx, query, 99, chainTransitionKind, "chain-unknown-version", "node-unknown-version"); err != nil {
				t.Fatalf("install unknown version: %v", err)
			}
			receipt, known, err := store.chainTransitionReceipt(ctx, "chain-unknown-version", "node-unknown-version")
			if !errors.Is(err, errUnsupportedTransitionReceipt) || known {
				t.Fatalf("unknown-version receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}
		})
	}
}
