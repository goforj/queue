package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/goforj/queue/busruntime"
)

// ChainBuilder configures and dispatches a sequential workflow.
type ChainBuilder interface {
	// OnQueue applies a default queue to chain jobs that do not set one.
	OnQueue(queue string) ChainBuilder
	// Catch registers a callback invoked when chain execution fails.
	Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder
	// Finally registers a callback invoked once when chain execution finishes.
	Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder
	// Dispatch creates and starts the chain workflow.
	Dispatch(ctx context.Context) (string, error)
}

type chainBuilder struct {
	r     *runtime
	jobs  []Job
	queue string
	catch func(ctx context.Context, st ChainState, err error) error
	done  func(ctx context.Context, st ChainState) error
}

type synchronousChainResultContextKey struct{}

type synchronousChainResult struct {
	mu  sync.Mutex
	err error
}

// withSynchronousChainResult lets inline continuation deliveries report their
// execution error without turning it into the predecessor's delivery outcome.
func withSynchronousChainResult(ctx context.Context) (context.Context, *synchronousChainResult) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := &synchronousChainResult{}
	return context.WithValue(ctx, synchronousChainResultContextKey{}, result), result
}

// record stores the first downstream error because it is the causal terminal
// outcome observed by the caller that started this inline chain execution.
func (r *synchronousChainResult) record(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

// executionError returns the exact downstream error so errors.Is and errors.As
// retain the application's original error chain.
func (r *synchronousChainResult) executionError() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// recordSynchronousChainError propagates an inline continuation failure to the
// chain dispatch boundary when the caller is still waiting for execution.
func recordSynchronousChainError(ctx context.Context, err error) {
	if ctx == nil {
		return
	}
	result, _ := ctx.Value(synchronousChainResultContextKey{}).(*synchronousChainResult)
	result.record(err)
}

// OnQueue supplies a target only for chain jobs that do not already select one.
func (b *chainBuilder) OnQueue(queue string) ChainBuilder {
	b.queue = queue
	return b
}

// Catch retains the explicitly ephemeral failure closure for this process lifetime.
func (b *chainBuilder) Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder {
	b.catch = fn
	return b
}

// Finally retains the explicitly ephemeral terminal closure for this process lifetime.
func (b *chainBuilder) Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder {
	b.done = fn
	return b
}

// Dispatch persists every node before enqueueing the first canonical delivery.
func (b *chainBuilder) Dispatch(ctx context.Context) (string, error) {
	if len(b.jobs) == 0 {
		return "", errors.New("chain requires at least one job")
	}
	ctx, synchronousResult := withSynchronousChainResult(ctx)
	chainID := newID("chn")
	dispatchID := newID("dsp")
	nodes := make([]ChainNode, 0, len(b.jobs))
	for i, job := range b.jobs {
		wj, err := toStoredJob(job)
		if err != nil {
			return "", err
		}
		if b.queue != "" && wj.Options.Queue == "" {
			wj.Options.Queue = b.queue
		}
		nodes = append(nodes, ChainNode{
			NodeID: nodeID(chainID, i),
			Job:    wj,
		})
	}
	if err := b.r.store.CreateChain(ctx, ChainRecord{
		ChainID:    chainID,
		DispatchID: dispatchID,
		Queue:      b.queue,
		Nodes:      nodes,
		CreatedAt:  b.r.now(),
	}); err != nil {
		return "", err
	}
	if !b.r.ephemeralCallbacksDisabled && (b.catch != nil || b.done != nil) {
		b.r.mu.Lock()
		b.r.chainCallbacks[chainID] = chainCallbacks{
			catch:   b.catch,
			finally: b.done,
		}
		b.r.mu.Unlock()
	}

	first := nodes[0]
	b.r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventChainStarted, DispatchID: dispatchID, ChainID: chainID, JobType: first.Job.Type, JobKey: storedJobEventKey(first.Job), Queue: first.Job.Options.Queue, Time: b.r.now()})
	if err := b.r.dispatchEnvelope(ctx, internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        first.NodeID,
		JobID:         newID("job"),
		Job:           first.Job,
	}); err != nil {
		if executionErr, ok := acceptedDispatchExecutionError(err); ok {
			return chainID, executionErr
		}
		_, owned, failErr := b.r.failChainNode(ctx, chainID, first.NodeID, err)
		if failErr != nil {
			return chainID, uncommittedMutationError("fail chain after initial dispatch rejection", errors.Join(err, failErr))
		}
		if !owned {
			return chainID, err
		}
		base := envelope{DispatchID: dispatchID, ChainID: chainID, Job: first.Job}
		b.r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventChainFailed, DispatchID: dispatchID, ChainID: chainID, JobType: first.Job.Type, JobKey: storedJobEventKey(first.Job), Queue: first.Job.Options.Queue, Time: b.r.now(), Err: err})
		_, stErr := b.r.store.GetChain(ctx, chainID)
		if stErr != nil {
			return chainID, errors.Join(err, uncommittedMutationError("read chain after initial dispatch rejection", stErr))
		}
		catchErr := b.r.invokeCallbackInline(ctx, base, "chain_catch", err)
		finallyErr := b.r.invokeCallbackInline(ctx, base, "chain_finally", nil)
		b.r.cleanupChainCallbacks(chainID)
		return chainID, errors.Join(err, catchErr, finallyErr)
	}
	if executionErr := synchronousResult.executionError(); executionErr != nil {
		return chainID, executionErr
	}
	return chainID, nil
}

type chainCallbacks struct {
	catch   func(ctx context.Context, st ChainState, err error) error
	finally func(ctx context.Context, st ChainState) error
}

// prepareChainSuccessCallbacks discards the failure-only closure before a successful terminal callback is scheduled.
func (r *runtime) prepareChainSuccessCallbacks(chainID string) {
	r.mu.Lock()
	callbacks, ok := r.chainCallbacks[chainID]
	if ok {
		callbacks.catch = nil
		r.chainCallbacks[chainID] = callbacks
	}
	r.mu.Unlock()
}

// finishChainCallback clears only the closure that ran so concurrently scheduled terminal callbacks remain available.
func (r *runtime) finishChainCallback(chainID, kind string) {
	r.mu.Lock()
	callbacks, ok := r.chainCallbacks[chainID]
	if ok {
		switch kind {
		case "catch":
			callbacks.catch = nil
		case "finally":
			callbacks.finally = nil
		}
		if callbacks.catch == nil && callbacks.finally == nil {
			delete(r.chainCallbacks, chainID)
		} else {
			r.chainCallbacks[chainID] = callbacks
		}
	}
	r.mu.Unlock()
}

// cleanupChainCallbacks removes terminal workflow entries that have no remaining configured closure.
func (r *runtime) cleanupChainCallbacks(chainID string) {
	r.mu.Lock()
	callbacks, ok := r.chainCallbacks[chainID]
	if ok && callbacks.catch == nil && callbacks.finally == nil {
		delete(r.chainCallbacks, chainID)
	}
	r.mu.Unlock()
}

// nodeID combines chain ownership with random entropy so persisted completion markers cannot collide across chains.
func nodeID(chainID string, idx int) string {
	return chainID + "_" + newID("n")
}

// failChainNode uses the additive atomic capability when available while
// retaining a state-confirmed fallback for established custom stores.
func (r *runtime) failChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error) {
	if store, ok := r.store.(outcomeStore); ok {
		return store.FailChainNode(ctx, chainID, nodeID, cause)
	}
	state, err := r.store.GetChain(ctx, chainID)
	if err != nil {
		return ChainState{}, false, err
	}
	owned, claimable, err := chainNodeFailureDisposition(state, nodeID)
	if err != nil || !claimable {
		return state, owned, err
	}
	if err := r.store.FailChain(ctx, chainID, cause); err != nil {
		return ChainState{}, false, err
	}
	state, err = r.store.GetChain(ctx, chainID)
	if err != nil {
		return ChainState{}, false, err
	}
	owned, claimable, err = chainNodeFailureDisposition(state, nodeID)
	if err != nil {
		return ChainState{}, false, err
	}
	if claimable {
		return ChainState{}, false, errors.New("chain store accepted failure without terminal state")
	}
	return state, owned, nil
}

// failChainNodeOutcome uses built-in receipt fencing when available while
// preserving the established arbitration behavior of decorated custom stores.
func (r *runtime) failChainNodeOutcome(ctx context.Context, chainID, nodeID string, cause error, claim transitionClaim) (chainFailureResult, error) {
	if store, ok := r.store.(chainFailureStore); ok {
		return store.failChainOutcome(ctx, chainID, nodeID, cause, claim)
	}
	state, owned, err := r.failChainNode(ctx, chainID, nodeID, cause)
	return chainFailureResult{state: state, owned: owned, claimedNow: owned}, err
}

// observedChainFailure preserves the committed cause across redelivery while
// retaining permanent classification without exposing a replayed cause.
func observedChainFailure(state ChainState, current error) error {
	if state.Failure == "" || (current != nil && current.Error() == state.Failure) {
		return current
	}
	committed := errors.New(state.Failure)
	if busruntime.IsPermanent(current) {
		return busruntime.Permanent(committed)
	}
	return committed
}

// advanceChainNode uses built-in atomic ownership when available while
// retaining the established Store projection for compatibility implementations.
func (r *runtime) advanceChainNode(ctx context.Context, chainID, nodeID string, claim transitionClaim) (chainAdvanceResult, error) {
	if store, ok := r.store.(chainAdvanceStore); ok {
		return store.advanceChainOutcome(ctx, chainID, nodeID, claim)
	}
	next, done, err := r.store.AdvanceChain(ctx, chainID, nodeID)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	return chainAdvanceResult{next: next, done: done, successOwned: true, claimedNow: true}, nil
}

// storedJobsEqual compares persisted protocol identity before recovery trusts
// an envelope to reconstruct externally visible job correlation.
func storedJobsEqual(left, right StoredJob) bool {
	return left.Type == right.Type && bytes.Equal(left.Payload, right.Payload) && left.Options == right.Options
}

// chainFactID includes retained-row correlation so one deterministic ID never
// labels different event payloads when duplicate physical envelopes disagree.
func chainFactID(kind EventKind, env envelope) string {
	return stableWorkflowFactID(kind, env.DispatchID, env.ChainID, env.NodeID, env.JobID, env.Job.Type, storedJobEventKey(env.Job), env.Job.Options.Queue)
}

// recoverCommittedChainSuccessor preserves a committed predecessor's live
// continuation without reconstructing facts that require exact receipt ownership.
func (r *runtime) recoverCommittedChainSuccessor(ctx context.Context, env envelope, state ChainState, index int) error {
	if state.Completed || state.Failed || state.NextIndex != index+1 {
		return nil
	}
	next := state.Nodes[state.NextIndex]
	return r.dispatchChainSuccessor(ctx, env, &next)
}

// recoverCommittedChainSuccess handles a reclaimed row before application code
// runs when durable state proves the node already succeeded. Facts require a
// receipt owned by the exact unsettled generation; application effects are not
// replayed, while an immediate still-pending continuation remains recoverable.
func (r *runtime) recoverCommittedChainSuccess(ctx context.Context, env envelope) (bool, error) {
	provenance, recovering := recoveredDeliveryProvenance(ctx)
	if !recovering {
		return false, nil
	}
	state, err := r.store.GetChain(ctx, env.ChainID)
	if err != nil {
		return true, uncommittedMutationError("recover committed chain success", err)
	}
	if state.ChainID != env.ChainID {
		return true, uncommittedMutationError("recover committed chain success", fmt.Errorf("requested chain %q returned state for %q", env.ChainID, state.ChainID))
	}
	if state.DispatchID != "" && env.DispatchID != "" && state.DispatchID != env.DispatchID {
		return true, uncommittedMutationError("recover committed chain success", fmt.Errorf("chain %q dispatch mismatch", env.ChainID))
	}
	index, known := chainNodePosition(state.Nodes, env.NodeID)
	if !known {
		return true, uncommittedMutationError("recover committed chain success", fmt.Errorf("chain %q does not contain node %q", env.ChainID, env.NodeID))
	}
	successOwned, err := chainNodeSuccessDisposition(state, env.NodeID)
	if err != nil {
		return true, uncommittedMutationError("recover committed chain success", err)
	}
	if !storedJobsEqual(state.Nodes[index].Job, env.Job) {
		return true, uncommittedMutationError("recover committed chain success", fmt.Errorf("chain %q node %q job mismatch", env.ChainID, env.NodeID))
	}
	if !successOwned {
		return false, nil
	}
	receiptStore, capable := r.store.(transitionReceiptStore)
	if !capable {
		return true, r.recoverCommittedChainSuccessor(ctx, env, state, index)
	}
	receipt, receiptKnown, receiptErr := receiptStore.chainTransitionReceipt(ctx, env.ChainID, env.NodeID)
	if receiptErr != nil {
		return true, uncommittedMutationError("recover committed chain success", receiptErr)
	}
	if !receiptKnown {
		return true, r.recoverCommittedChainSuccessor(ctx, env, state, index)
	}
	if receipt.workflowKind != chainTransitionKind || receipt.workflowID != env.ChainID || receipt.memberID != env.NodeID || receipt.workflowDispatchID != state.DispatchID || !receipt.workflowCreatedAt.Equal(state.CreatedAt) {
		return true, uncommittedMutationError("recover committed chain success", errors.New("transition receipt does not match chain state"))
	}
	if err := validateRecoveredTransitionReceipt(env, receipt, false); err != nil {
		return true, uncommittedMutationError("recover committed chain success", err)
	}
	if receipt.outcome != BatchJobSucceeded {
		return true, uncommittedMutationError("recover committed chain success", errors.New("transition receipt does not own success"))
	}
	if receipt.aggregateCancelled {
		return true, uncommittedMutationError("recover committed chain success", errors.New("successful chain receipt cannot own cancellation"))
	}
	finalNode := index == len(state.Nodes)-1
	if receipt.aggregateCompleted != finalNode {
		return true, uncommittedMutationError("recover committed chain success", errors.New("transition receipt completion does not match chain node position"))
	}
	if !transitionReceiptOwnsRecoveredFacts(env, receipt, provenance) {
		return true, r.recoverCommittedChainSuccessor(ctx, env, state, index)
	}
	committedOutcome, recoveryErr := recoveredStoredJobSuccess(storedJobOutcome{env: env}, receipt, r.now())
	if recoveryErr != nil {
		return true, uncommittedMutationError("recover committed chain success", recoveryErr)
	}
	if finalNode {
		r.emitStoredJobOutcome(ctx, committedOutcome)
		r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: chainFactID(EventChainCompleted, env), Kind: EventChainCompleted, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
		return true, nil
	}

	r.emitStoredJobOutcome(ctx, committedOutcome)
	r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: chainFactID(EventChainAdvanced, env), Kind: EventChainAdvanced, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
	if !state.Completed && !state.Failed && state.NextIndex == index+1 {
		next := state.Nodes[state.NextIndex]
		return true, r.dispatchChainSuccessor(ctx, env, &next)
	}
	return true, nil
}

// recoverCommittedChainFailure settles a reclaimed terminal failure from its
// persisted cause without fabricating another handler occurrence or callback.
func (r *runtime) recoverCommittedChainFailure(ctx context.Context, env envelope) (bool, error) {
	_, recovering := recoveredDeliveryProvenance(ctx)
	if !recovering {
		return false, nil
	}
	state, err := r.store.GetChain(ctx, env.ChainID)
	if err != nil {
		return true, uncommittedMutationError("recover committed chain failure", err)
	}
	if state.ChainID != env.ChainID {
		return true, uncommittedMutationError("recover committed chain failure", fmt.Errorf("requested chain %q returned state for %q", env.ChainID, state.ChainID))
	}
	if state.DispatchID != "" && env.DispatchID != "" && state.DispatchID != env.DispatchID {
		return true, uncommittedMutationError("recover committed chain failure", fmt.Errorf("chain %q dispatch mismatch", env.ChainID))
	}
	if !state.Failed || state.Completed {
		return false, nil
	}
	index, known := chainNodePosition(state.Nodes, env.NodeID)
	if !known {
		return true, uncommittedMutationError("recover committed chain failure", fmt.Errorf("chain %q does not contain node %q", env.ChainID, env.NodeID))
	}
	owned, _, err := chainNodeFailureDisposition(state, env.NodeID)
	if err != nil {
		return true, uncommittedMutationError("recover committed chain failure", err)
	}
	if !owned {
		return false, nil
	}
	if !storedJobsEqual(state.Nodes[index].Job, env.Job) {
		return true, uncommittedMutationError("recover committed chain failure", fmt.Errorf("chain %q node %q job mismatch", env.ChainID, env.NodeID))
	}
	receiptStore, capable := r.store.(transitionReceiptStore)
	if !capable {
		return false, nil
	}
	receipt, receiptKnown, receiptErr := receiptStore.chainTransitionReceipt(ctx, env.ChainID, env.NodeID)
	if receiptErr != nil {
		return true, uncommittedMutationError("recover committed chain failure", receiptErr)
	}
	if !receiptKnown {
		return false, nil
	}
	if receipt.workflowKind != chainTransitionKind || receipt.workflowID != env.ChainID || receipt.memberID != env.NodeID || receipt.workflowDispatchID != state.DispatchID || !receipt.workflowCreatedAt.Equal(state.CreatedAt) {
		return true, uncommittedMutationError("recover committed chain failure", errors.New("transition receipt does not match chain state"))
	}
	if receipt.outcome != BatchJobFailed {
		return true, uncommittedMutationError("recover committed chain failure", errors.New("transition receipt does not own failure"))
	}
	if receipt.aggregateCompleted || receipt.aggregateCancelled {
		return true, uncommittedMutationError("recover committed chain failure", errors.New("failed chain receipt cannot own completion"))
	}
	if err := validateRecoveredTransitionReceipt(env, receipt, false); err != nil {
		return true, uncommittedMutationError("recover committed chain failure", err)
	}
	if state.Failure == "" {
		return true, busruntime.Permanent(fmt.Errorf("chain %q node %q was already committed as failed; original cause was empty", env.ChainID, env.NodeID))
	}
	return true, busruntime.Permanent(errors.New(state.Failure))
}

// dispatchChainSuccessor retains at-least-once continuation recovery after a
// predecessor transition committed but its first enqueue did not complete.
// A surviving predecessor cannot distinguish a missing successor from one
// already enqueued but not yet progressed, so recovery may enqueue a duplicate
// under the queue's existing at-least-once contract.
func (r *runtime) dispatchChainSuccessor(ctx context.Context, env envelope, next *ChainNode) error {
	if next == nil {
		return uncommittedMutationError("dispatch next chain node", errors.New("chain store omitted successor"))
	}
	dispatchErr := r.dispatchEnvelope(ctx, internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    env.DispatchID,
		Kind:          "chain_node",
		ChainID:       env.ChainID,
		NodeID:        next.NodeID,
		JobID:         newID("job"),
		Job:           next.Job,
	})
	if executionErr, ok := acceptedDispatchExecutionError(dispatchErr); ok {
		recordSynchronousChainError(ctx, executionErr)
		return nil
	}
	if dispatchErr != nil {
		return uncommittedMutationError("dispatch next chain node", dispatchErr)
	}
	return nil
}

// handleInternalChainNode advances or fails a chain only after its application attempt reaches a committable outcome.
func (r *runtime) handleInternalChainNode(ctx context.Context, job busruntime.InboundJob) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	if _, recovering := recoveredDeliveryProvenance(ctx); recovering {
		applyDeliveryAttempt(ctx, &env)
		handled, recoveryErr := r.recoverCommittedChainFailure(ctx, env)
		if recoveryErr != nil || handled {
			return recoveryErr
		}
		handled, recoveryErr = r.recoverCommittedChainSuccess(ctx, env)
		if recoveryErr != nil || handled {
			return recoveryErr
		}
	}
	outcome := r.executeStoredJobAttempt(ctx, env)
	switch busruntime.ClassifyAttempt(outcome.attempt, outcome.err) {
	case busruntime.AttemptRetry, busruntime.AttemptRedeliver:
		return outcome.err
	case busruntime.AttemptFailed:
		failed, markErr := r.failChainNodeOutcome(ctx, env.ChainID, env.NodeID, outcome.err, transitionClaimFromOutcome(ctx, outcome))
		if markErr != nil {
			return uncommittedMutationError("fail chain", markErr)
		}
		markDeliveryTransitionCommitted(ctx, failed.claimedNow, failed.receiptKnown)
		if !failed.owned {
			if _, recovered := recoveredDeliveryProvenance(ctx); !recovered {
				return nil
			}
			recovered, recoveryErr := r.recoverCommittedChainFailure(ctx, outcome.env)
			if recoveryErr != nil || recovered {
				return recoveryErr
			}
			recovered, recoveryErr = r.recoverCommittedChainSuccess(ctx, outcome.env)
			if recoveryErr != nil || recovered {
				return recoveryErr
			}
			return nil
		}
		if !failed.claimedNow {
			if _, recovered := recoveredDeliveryProvenance(ctx); recovered {
				recovered, recoveryErr := r.recoverCommittedChainFailure(ctx, outcome.env)
				if recoveryErr != nil || recovered {
					return recoveryErr
				}
				return outcome.err
			}
			return nil
		}
		state := failed.state
		if state.Completed {
			return nil
		}
		if !state.Failed {
			return uncommittedMutationError("confirm chain failure", errors.New("chain store accepted failure without terminal state"))
		}
		observedErr := observedChainFailure(state, outcome.err)
		observedOutcome := outcome
		observedOutcome.err = observedErr
		r.emitStoredJobOutcome(ctx, observedOutcome)
		r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventChainFailed, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now(), Err: observedErr})
		_ = r.dispatchCallback(ctx, env, "chain_catch", observedErr)
		_ = r.dispatchCallback(ctx, env, "chain_finally", nil)
		r.cleanupChainCallbacks(env.ChainID)
		return outcome.err
	}
	advance, advErr := r.advanceChainNode(ctx, env.ChainID, env.NodeID, transitionClaimFromOutcome(ctx, outcome))
	if advErr != nil {
		return uncommittedMutationError("advance chain", advErr)
	}
	markDeliveryTransitionCommitted(ctx, advance.claimedNow, advance.receiptKnown)
	_, recovering := recoveredDeliveryProvenance(ctx)
	if !advance.claimedNow {
		if !advance.successOwned {
			return nil
		}
		if recovering {
			recovered, recoveryErr := r.recoverCommittedChainSuccess(ctx, outcome.env)
			if recoveryErr != nil || recovered {
				return recoveryErr
			}
			return uncommittedMutationError("recover committed chain success", errors.New("store reported success ownership without advanced state"))
		}
		if advance.done {
			state := advance.state
			index, known := chainNodePosition(state.Nodes, env.NodeID)
			if known && state.Completed && index == len(state.Nodes)-1 {
				r.prepareChainSuccessCallbacks(env.ChainID)
				_ = r.dispatchCallback(ctx, env, "chain_finally", nil)
				r.cleanupChainCallbacks(env.ChainID)
			}
			return nil
		}
		return r.dispatchChainSuccessor(ctx, env, advance.next)
	}
	next, done := advance.next, advance.done
	if done {
		state := advance.state
		if state.ChainID == "" {
			var stateErr error
			state, stateErr = r.store.GetChain(ctx, env.ChainID)
			if stateErr != nil {
				return uncommittedMutationError("confirm chain completion", stateErr)
			}
		}
		// Old SQL stores could record failure after completion, so completion
		// retains precedence for those otherwise-unreachable dual-terminal rows.
		if !state.Completed && state.Failed {
			return nil
		}
		if !state.Completed {
			return uncommittedMutationError("confirm chain completion", errors.New("chain store returned done without terminal state"))
		}
		index, known := chainNodePosition(state.Nodes, env.NodeID)
		if !known {
			return uncommittedMutationError("confirm chain completion", errors.New("chain store returned done for an unknown node"))
		}
		if index != len(state.Nodes)-1 {
			return nil
		}
		r.emitStoredJobOutcome(ctx, outcome)
		r.prepareChainSuccessCallbacks(env.ChainID)
		r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: chainFactID(EventChainCompleted, env), Kind: EventChainCompleted, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
		_ = r.dispatchCallback(ctx, env, "chain_finally", nil)
		r.cleanupChainCallbacks(env.ChainID)
		return nil
	}
	r.emitStoredJobOutcome(ctx, outcome)
	r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: chainFactID(EventChainAdvanced, env), Kind: EventChainAdvanced, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
	return r.dispatchChainSuccessor(ctx, env, next)
}

// invokeChainCatch claims the ephemeral catch callback before application code can run.
func (r *runtime) invokeChainCatch(ctx context.Context, st ChainState, err error) error {
	return r.invokeChainCatchObserved(ctx, st, err, nil)
}

// invokeChainCatchObserved emits lifecycle start only after state validation and idempotency claim succeed.
func (r *runtime) invokeChainCatchObserved(ctx context.Context, st ChainState, err error, onClaimed func()) error {
	if !st.Failed || st.Completed {
		return errCallbackNotReady
	}
	key := "chain_catch:" + st.ChainID
	ok, onceErr := r.callbackOnce(ctx, key)
	if onceErr != nil {
		return onceErr
	}
	if !ok {
		return errCallbackAlreadyInvoked
	}
	if onClaimed != nil {
		onClaimed()
	}
	r.mu.RLock()
	cb := r.chainCallbacks[st.ChainID]
	r.mu.RUnlock()
	if cb.catch != nil {
		defer r.finishChainCallback(st.ChainID, "catch")
		return runEphemeralCallback(func() error { return cb.catch(ctx, st, err) })
	}
	return errCallbackUnavailable
}

// invokeChainFinally claims the terminal closure before application code can run.
func (r *runtime) invokeChainFinally(ctx context.Context, st ChainState) error {
	return r.invokeChainFinallyObserved(ctx, st, nil)
}

// invokeChainFinallyObserved emits lifecycle start only after state validation and idempotency claim succeed.
func (r *runtime) invokeChainFinallyObserved(ctx context.Context, st ChainState, onClaimed func()) error {
	if !st.Failed && !st.Completed {
		return errCallbackNotReady
	}
	key := "chain_finally:" + st.ChainID
	ok, onceErr := r.callbackOnce(ctx, key)
	if onceErr != nil {
		return onceErr
	}
	if !ok {
		return errCallbackAlreadyInvoked
	}
	if onClaimed != nil {
		onClaimed()
	}
	r.mu.RLock()
	cb := r.chainCallbacks[st.ChainID]
	r.mu.RUnlock()
	if cb.finally == nil {
		return errCallbackUnavailable
	}
	defer r.finishChainCallback(st.ChainID, "finally")
	return runEphemeralCallback(func() error { return cb.finally(ctx, st) })
}
