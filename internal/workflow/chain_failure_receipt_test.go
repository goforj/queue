package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

// TestChainCommittedFailureRecoveryPreservesOneApplicationOccurrence proves a
// receipt recovery archives the durable cause without replaying work or facts.
func TestChainCommittedFailureRecoveryPreservesOneApplicationOccurrence(t *testing.T) {
	const (
		chainID    = "chain-committed-failure-recovery"
		nodeID     = "node-committed-failure-recovery"
		dispatchID = "dispatch-committed-failure-recovery"
		jobID      = "job-committed-failure-recovery"
		jobType    = "workflow:chain:committed-failure-recovery"
		owner      = "generation-chain-committed-failure"
	)
	store := NewMemoryStore()
	env := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "chain_node", ChainID: chainID, NodeID: nodeID, JobID: jobID, Job: StoredJob{Type: jobType, Payload: []byte(`{"id":1}`)}}
	if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: []ChainNode{{NodeID: nodeID, Job: env.Job}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	committedCause := errors.New("persisted terminal chain cause")
	var handlerCalls, catchCalls, finallyCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		return busruntime.Permanent(committedCause)
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

	firstContext, firstSettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
	firstContext = workflowGenerationContext(firstContext, owner)
	if err := queueRuntime.DispatchJSON(firstContext, internalJobChainNode, env); !errors.Is(err, committedCause) || !busruntime.IsPermanent(err) {
		t.Fatalf("initial failure = %v, want permanent committed cause", err)
	}
	if !firstSettlement.ApplicationStateCommitted() {
		t.Fatal("initial failure did not signal committed application state")
	}
	receipt, known, err := requireTransitionReceiptStore(t, store).chainTransitionReceipt(context.Background(), chainID, nodeID)
	if err != nil || !known || receipt.owner != workflowTransitionClaim(env, 2, owner) || receipt.outcome != BatchJobFailed || receipt.aggregateCompleted || receipt.aggregateCancelled {
		t.Fatalf("failed chain receipt = known:%t receipt:%+v err:%v", known, receipt, err)
	}
	if handlerCalls != 1 || catchCalls != 1 || finallyCalls != 1 {
		t.Fatalf("initial handler/catch/finally calls = %d/%d/%d, want 1/1/1", handlerCalls, catchCalls, finallyCalls)
	}
	initialEvents := len(recorder.events)
	if countWorkflowEvents(recorder.events, EventJobFailed) != 1 || countWorkflowEvents(recorder.events, EventChainFailed) != 1 {
		t.Fatalf("initial job/chain failure facts = %d/%d, want 1/1", countWorkflowEvents(recorder.events, EventJobFailed), countWorkflowEvents(recorder.events, EventChainFailed))
	}

	provenance := []struct {
		name                string
		recoveredGeneration string
	}{
		{name: "exact owner", recoveredGeneration: owner},
		{name: "different owner", recoveredGeneration: "generation-chain-different-owner"},
		{name: "legacy recovery"},
	}
	for index, test := range provenance {
		t.Run(test.name, func(t *testing.T) {
			recoveryContext, recoverySettlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
			recoveryContext = workflowRecoveryContext(recoveryContext, "generation-chain-recovery-"+test.name, test.recoveredGeneration)
			recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobChainNode, env)
			if recoveryErr == nil || recoveryErr.Error() != committedCause.Error() || !busruntime.IsPermanent(recoveryErr) {
				t.Fatalf("recovery %d error = %v, want permanent persisted cause", index, recoveryErr)
			}
			if recoverySettlement.ApplicationStateCommitted() {
				t.Fatal("read-only failure recovery signaled a new application mutation")
			}
			if handlerCalls != 1 || catchCalls != 1 || finallyCalls != 1 || len(recorder.events) != initialEvents {
				t.Fatalf("recovery occurrence changed handler/catch/finally/events = %d/%d/%d/%d, want 1/1/1/%d", handlerCalls, catchCalls, finallyCalls, len(recorder.events), initialEvents)
			}
		})
	}
}

// TestChainFailureRecoveryRejectsInvalidReceiptIdentity proves every durable
// identity and terminal-shape mismatch fails closed before application code.
func TestChainFailureRecoveryRejectsInvalidReceiptIdentity(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*transitionReceipt)
	}{
		{name: "receipt version", mutate: func(receipt *transitionReceipt) { receipt.version++ }},
		{name: "event schema", mutate: func(receipt *transitionReceipt) { receipt.eventSchemaVersion++ }},
		{name: "success outcome", mutate: func(receipt *transitionReceipt) { receipt.outcome = BatchJobSucceeded }},
		{name: "completion owner", mutate: func(receipt *transitionReceipt) { receipt.aggregateCompleted = true }},
		{name: "cancellation owner", mutate: func(receipt *transitionReceipt) { receipt.aggregateCancelled = true }},
		{name: "empty delivery owner", mutate: func(receipt *transitionReceipt) { receipt.owner.deliveryID = "" }},
		{name: "negative owner attempt", mutate: func(receipt *transitionReceipt) { receipt.owner.attempt = -1 }},
		{name: "job dispatch", mutate: func(receipt *transitionReceipt) { receipt.owner.dispatchID = "dispatch-other" }},
		{name: "empty owner job id", mutate: func(receipt *transitionReceipt) { receipt.owner.jobID = "" }},
		{name: "job fingerprint", mutate: func(receipt *transitionReceipt) { receipt.owner.jobFingerprint = "fingerprint-other" }},
		{name: "workflow dispatch", mutate: func(receipt *transitionReceipt) { receipt.workflowDispatchID = "workflow-dispatch-other" }},
		{name: "workflow creation", mutate: func(receipt *transitionReceipt) {
			receipt.workflowCreatedAt = receipt.workflowCreatedAt.Add(time.Second)
		}},
		{name: "workflow kind", mutate: func(receipt *transitionReceipt) { receipt.workflowKind = batchTransitionKind }},
		{name: "workflow id", mutate: func(receipt *transitionReceipt) { receipt.workflowID = "chain-other" }},
		{name: "member id", mutate: func(receipt *transitionReceipt) { receipt.memberID = "node-other" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			const (
				chainID    = "chain-invalid-failure-receipt"
				nodeID     = "node-invalid-failure-receipt"
				dispatchID = "dispatch-invalid-failure-receipt"
				jobID      = "job-invalid-failure-receipt"
				jobType    = "workflow:chain:invalid-failure-receipt"
				owner      = "generation-invalid-failure-receipt"
			)
			store := NewMemoryStore().(*memoryStore)
			env := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "chain_node", ChainID: chainID, NodeID: nodeID, JobID: jobID, Job: StoredJob{Type: jobType, Payload: []byte(`{"id":2}`)}}
			if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: []ChainNode{{NodeID: nodeID, Job: env.Job}}}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if result, err := store.failChainOutcome(context.Background(), chainID, nodeID, errors.New("persisted failure"), workflowTransitionClaim(env, 2, owner)); err != nil || !result.receiptKnown {
				t.Fatalf("commit failed chain = %+v err:%v", result, err)
			}
			key := transitionReceiptKey{workflowKind: chainTransitionKind, workflowID: chainID, memberID: nodeID}
			store.mu.Lock()
			receipt := store.transitionReceipts[key]
			test.mutate(&receipt)
			store.transitionReceipts[key] = receipt
			store.mu.Unlock()

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, callbackCalls int
			runtime.Register(jobType, func(context.Context, Context) error {
				handlerCalls++
				return busruntime.Permanent(errors.New("unexpected replay"))
			})
			runtime.chainCallbacks[chainID] = chainCallbacks{finally: func(context.Context, ChainState) error {
				callbackCalls++
				return nil
			}}
			recoveryContext, settlement := busruntime.WithDeliverySettlement(exhaustedWorkflowContext())
			recoveryContext = workflowRecoveryContext(recoveryContext, "generation-invalid-recovery", owner)
			recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobChainNode, env)
			if !busruntime.IsUncommitted(recoveryErr) {
				t.Fatalf("invalid receipt recovery error = %v, want uncommitted", recoveryErr)
			}
			if handlerCalls != 0 || callbackCalls != 0 || settlement.ApplicationStateCommitted() || len(recorder.events) != 0 {
				t.Fatalf("invalid receipt handler/callback/committed/events = %d/%d/%t/%d, want 0/0/false/0", handlerCalls, callbackCalls, settlement.ApplicationStateCommitted(), len(recorder.events))
			}
		})
	}
}

// TestChainFailureRecoveryAllowsDifferentPhysicalDeliveryIdentity proves a
// logical failure receipt archives duplicate jobs and attempts without replay.
func TestChainFailureRecoveryAllowsDifferentPhysicalDeliveryIdentity(t *testing.T) {
	for _, test := range []struct {
		name           string
		currentAttempt int
		currentJobID   string
	}{
		{name: "different physical job", currentAttempt: 2, currentJobID: "job-chain-failure-duplicate"},
		{name: "different physical attempt", currentAttempt: 3, currentJobID: "job-chain-failure-owner"},
		{name: "negative current attempt", currentAttempt: -1, currentJobID: "job-chain-failure-owner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const (
				chainID    = "chain-failure-physical-nonowner"
				nodeID     = "node-chain-failure-physical-nonowner"
				dispatchID = "dispatch-chain-failure-physical-nonowner"
				jobType    = "workflow:chain:failure-physical-nonowner"
				owner      = "generation-chain-failure-physical-owner"
			)
			store := NewMemoryStore()
			ownerEnv := envelope{SchemaVersion: schemaVersion, DispatchID: dispatchID, Kind: "chain_node", ChainID: chainID, NodeID: nodeID, JobID: "job-chain-failure-owner", Job: StoredJob{Type: jobType, Payload: []byte(`{"id":3}`)}}
			if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: []ChainNode{{NodeID: nodeID, Job: ownerEnv.Job}}}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			committedCause := errors.New("persisted duplicate-delivery chain failure")
			if result, err := requireChainFailureStore(t, store).failChainOutcome(context.Background(), chainID, nodeID, committedCause, workflowTransitionClaim(ownerEnv, 2, owner)); err != nil || !result.receiptKnown {
				t.Fatalf("commit failed chain = %+v err:%v", result, err)
			}

			runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
			var handlerCalls, callbackCalls int
			runtime.Register(jobType, func(context.Context, Context) error {
				handlerCalls++
				return busruntime.Permanent(errors.New("duplicate application execution"))
			})
			runtime.chainCallbacks[chainID] = chainCallbacks{finally: func(context.Context, ChainState) error {
				callbackCalls++
				return nil
			}}
			currentEnv := ownerEnv
			currentEnv.JobID = test.currentJobID
			attemptContext := busruntime.WithDeliveryAttempt(context.Background(), busruntime.DeliveryAttempt{Number: test.currentAttempt, MaxRetry: 3})
			recoveryContext, settlement := busruntime.WithDeliverySettlement(attemptContext)
			recoveryContext = workflowRecoveryContext(recoveryContext, "generation-chain-failure-current", owner)
			recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobChainNode, currentEnv)
			if recoveryErr == nil || recoveryErr.Error() != committedCause.Error() || !busruntime.IsPermanent(recoveryErr) || busruntime.IsUncommitted(recoveryErr) {
				t.Fatalf("physical nonowner recovery error = %v, want persisted permanent cause", recoveryErr)
			}
			settlement.Commit()
			if handlerCalls != 0 || callbackCalls != 0 || settlement.ApplicationStateCommitted() || len(recorder.events) != 0 {
				t.Fatalf("handler/callback/committed/events = %d/%d/%t/%d, want 0/0/false/0", handlerCalls, callbackCalls, settlement.ApplicationStateCommitted(), len(recorder.events))
			}
		})
	}
}

// TestChainLegacyFailureRecoveryRetainsCurrentFailureClassification proves a
// receipt-absent built-in row cannot turn a terminal replay into success.
func TestChainLegacyFailureRecoveryRetainsCurrentFailureClassification(t *testing.T) {
	const (
		chainID = "chain-legacy-failure-classification"
		nodeID  = "node-legacy-failure-classification"
		jobType = "workflow:chain:legacy-failure-classification"
	)
	store := NewMemoryStore()
	env := envelope{SchemaVersion: schemaVersion, DispatchID: "dispatch-legacy-failure-classification", Kind: "chain_node", ChainID: chainID, NodeID: nodeID, JobID: "job-legacy-failure-classification", Job: StoredJob{Type: jobType}}
	if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: env.DispatchID, Nodes: []ChainNode{{NodeID: nodeID, Job: env.Job}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if _, owned, err := requireOutcomeStore(t, store).FailChainNode(context.Background(), chainID, nodeID, errors.New("legacy committed cause")); err != nil || !owned {
		t.Fatalf("fail legacy chain = owned:%t err:%v", owned, err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	currentCause := errors.New("current terminal replay")
	var handlerCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		return busruntime.Permanent(currentCause)
	})
	recoveryContext := workflowRecoveryContext(exhaustedWorkflowContext(), "generation-legacy-current", "generation-legacy-absent")
	recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobChainNode, env)
	if !errors.Is(recoveryErr, currentCause) || !busruntime.IsPermanent(recoveryErr) {
		t.Fatalf("legacy recovery error = %v, want current permanent failure", recoveryErr)
	}
	if handlerCalls != 1 || countWorkflowEvents(recorder.events, EventJobFailed) != 0 || countWorkflowEvents(recorder.events, EventChainFailed) != 0 {
		t.Fatalf("legacy recovery handler/job/chain failure facts = %d/%d/%d, want 1/0/0", handlerCalls, countWorkflowEvents(recorder.events, EventJobFailed), countWorkflowEvents(recorder.events, EventChainFailed))
	}
}

// TestChainFailureRecoveryWithoutPersistedCauseUsesTerminalDiagnostic proves
// even an empty legacy cause remains a failed physical settlement.
func TestChainFailureRecoveryWithoutPersistedCauseUsesTerminalDiagnostic(t *testing.T) {
	const (
		chainID = "chain-empty-persisted-failure"
		nodeID  = "node-empty-persisted-failure"
		jobType = "workflow:chain:empty-persisted-failure"
		owner   = "generation-empty-persisted-failure"
	)
	store := NewMemoryStore().(*memoryStore)
	env := envelope{SchemaVersion: schemaVersion, DispatchID: "dispatch-empty-persisted-failure", Kind: "chain_node", ChainID: chainID, NodeID: nodeID, JobID: "job-empty-persisted-failure", Job: StoredJob{Type: jobType}}
	if err := store.CreateChain(context.Background(), ChainRecord{ChainID: chainID, DispatchID: env.DispatchID, Nodes: []ChainNode{{NodeID: nodeID, Job: env.Job}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if result, err := store.failChainOutcome(context.Background(), chainID, nodeID, nil, workflowTransitionClaim(env, 2, owner)); err != nil || !result.receiptKnown || result.state.Failure != "" {
		t.Fatalf("commit empty-cause failure = %+v err:%v", result, err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	var handlerCalls int
	runtime.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	recoveryContext := workflowRecoveryContext(exhaustedWorkflowContext(), "generation-empty-cause-recovery", owner)
	recoveryErr := queueRuntime.DispatchJSON(recoveryContext, internalJobChainNode, env)
	if recoveryErr == nil || !busruntime.IsPermanent(recoveryErr) || !strings.Contains(recoveryErr.Error(), "original cause was empty") {
		t.Fatalf("empty-cause recovery error = %v, want permanent diagnostic", recoveryErr)
	}
	if handlerCalls != 0 || len(recorder.events) != 0 {
		t.Fatalf("empty-cause recovery handler/events = %d/%d, want 0/0", handlerCalls, len(recorder.events))
	}
}

// countWorkflowEvents counts one event kind in a focused synchronous fixture.
func countWorkflowEvents(events []Event, kind EventKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}
