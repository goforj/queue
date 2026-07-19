package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/queue/busruntime"
)

// BatchBuilder configures and dispatches an aggregate workflow.
type BatchBuilder interface {
	// Name sets a display name for the batch.
	Name(name string) BatchBuilder
	// OnQueue applies a default queue to batch jobs that do not set one.
	OnQueue(queue string) BatchBuilder
	// AllowFailures keeps the batch running when individual jobs fail.
	AllowFailures() BatchBuilder
	// Progress registers a callback invoked as jobs complete.
	Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	// Then registers a callback invoked once when batch succeeds.
	Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	// Catch registers a callback invoked when batch encounters a failure.
	Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder
	// Finally registers a callback invoked once when batch reaches terminal state.
	Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	// Dispatch creates and starts the batch workflow.
	Dispatch(ctx context.Context) (string, error)
}

type batchBuilder struct {
	r           *runtime
	jobs        []Job
	name        string
	queue       string
	allowFailed bool
	progress    func(ctx context.Context, st BatchState) error
	then        func(ctx context.Context, st BatchState) error
	catch       func(ctx context.Context, st BatchState, err error) error
	finally     func(ctx context.Context, st BatchState) error
}

// Name retains an application-facing label alongside persisted batch state.
func (b *batchBuilder) Name(name string) BatchBuilder { b.name = name; return b }

// OnQueue supplies a target only for batch jobs that do not already select one.
func (b *batchBuilder) OnQueue(queue string) BatchBuilder {
	b.queue = queue
	return b
}

// AllowFailures records that terminal member failures should not cancel remaining work.
func (b *batchBuilder) AllowFailures() BatchBuilder {
	b.allowFailed = true
	return b
}

// Progress retains the explicitly ephemeral progress closure for this process lifetime.
func (b *batchBuilder) Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	b.progress = fn
	return b
}

// Then retains the explicitly ephemeral successful-terminal closure for this process lifetime.
func (b *batchBuilder) Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	b.then = fn
	return b
}

// Catch retains the explicitly ephemeral failure closure for this process lifetime.
func (b *batchBuilder) Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder {
	b.catch = fn
	return b
}

// Finally retains the explicitly ephemeral terminal closure for this process lifetime.
func (b *batchBuilder) Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	b.finally = fn
	return b
}

// Dispatch persists the complete batch before enqueueing its canonical jobs.
func (b *batchBuilder) Dispatch(ctx context.Context) (string, error) {
	if len(b.jobs) == 0 {
		return "", errors.New("batch requires at least one job")
	}
	batchID := newID("bat")
	dispatchID := newID("dsp")
	jobs := make([]BatchJob, 0, len(b.jobs))
	for _, job := range b.jobs {
		wj, err := toStoredJob(job)
		if err != nil {
			return "", err
		}
		if b.queue != "" && wj.Options.Queue == "" {
			wj.Options.Queue = b.queue
		}
		jobs = append(jobs, BatchJob{
			JobID: newID("job"),
			Job:   wj,
		})
	}
	if err := b.r.store.CreateBatch(ctx, BatchRecord{
		BatchID:     batchID,
		DispatchID:  dispatchID,
		Name:        b.name,
		Queue:       b.queue,
		AllowFailed: b.allowFailed,
		Jobs:        jobs,
		CreatedAt:   b.r.now(),
	}); err != nil {
		return "", err
	}

	if !b.r.ephemeralCallbacksDisabled && (b.progress != nil || b.then != nil || b.catch != nil || b.finally != nil) {
		b.r.mu.Lock()
		b.r.batchCallbacks[batchID] = batchCallbacks{
			progress: b.progress,
			then:     b.then,
			catch:    b.catch,
			finally:  b.finally,
		}
		b.r.mu.Unlock()
	}

	first := jobs[0]
	b.r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventBatchStarted, DispatchID: dispatchID, BatchID: batchID, JobType: first.Job.Type, JobKey: storedJobEventKey(first.Job), Queue: first.Job.Options.Queue, Time: b.r.now()})
	var synchronousErr error
	for _, job := range jobs {
		if err := b.r.dispatchEnvelope(ctx, internalJobBatchJob, envelope{
			SchemaVersion: schemaVersion,
			DispatchID:    dispatchID,
			Kind:          "batch_job",
			BatchID:       batchID,
			JobID:         job.JobID,
			Job:           job.Job,
		}); err != nil {
			if executionErr, ok := acceptedDispatchExecutionError(err); ok {
				if synchronousErr == nil {
					synchronousErr = executionErr
				}
				if b.allowFailed && !busruntime.IsUncommitted(executionErr) {
					continue
				}
				return batchID, executionErr
			}
			if st, stErr := b.r.store.GetBatch(ctx, batchID); stErr == nil && (st.Completed || st.Processed > 0 || st.Failed > 0) {
				return batchID, err
			}
			if cancelErr := b.r.store.CancelBatch(ctx, batchID); cancelErr != nil {
				return batchID, uncommittedMutationError("cancel batch after initial dispatch rejection", errors.Join(err, cancelErr))
			}
			base := envelope{DispatchID: dispatchID, BatchID: batchID, Job: job.Job}
			b.r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventBatchFailed, DispatchID: dispatchID, BatchID: batchID, JobID: job.JobID, JobType: job.Job.Type, JobKey: storedJobEventKey(job.Job), Queue: job.Job.Options.Queue, Time: b.r.now(), Err: err})
			b.r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventBatchCancelled, DispatchID: dispatchID, BatchID: batchID, JobID: job.JobID, JobType: job.Job.Type, JobKey: storedJobEventKey(job.Job), Queue: job.Job.Options.Queue, Time: b.r.now()})
			st, stErr := b.r.store.GetBatch(ctx, batchID)
			if stErr != nil {
				return batchID, errors.Join(err, uncommittedMutationError("read batch after initial dispatch rejection", stErr))
			}
			b.r.prepareBatchTerminalCallbacks(batchID, false, st.Failed > 0)
			catchErr := b.r.invokeCallbackInline(ctx, base, "batch_catch", err)
			finallyErr := b.r.invokeCallbackInline(ctx, base, "batch_finally", nil)
			b.r.cleanupBatchCallbacks(batchID)
			return batchID, errors.Join(err, catchErr, finallyErr)
		}
	}
	return batchID, synchronousErr
}

type batchCallbacks struct {
	progress func(ctx context.Context, st BatchState) error
	then     func(ctx context.Context, st BatchState) error
	catch    func(ctx context.Context, st BatchState, err error) error
	finally  func(ctx context.Context, st BatchState) error
}

// errCallbackAlreadyInvoked suppresses duplicate terminal facts when a broker redelivers an already-claimed ephemeral callback.
var errCallbackAlreadyInvoked = errors.New("workflow callback already invoked")

// errCallbackUnavailable reports an ephemeral callback whose owning process state no longer exists.
var errCallbackUnavailable = errors.New("workflow callback is unavailable")

// errCallbackNotReady rejects callback delivery before its workflow reaches the required state.
var errCallbackNotReady = errors.New("workflow callback state is not ready")

// prepareBatchTerminalCallbacks discards closures that cannot run for the selected terminal outcome.
func (r *runtime) prepareBatchTerminalCallbacks(batchID string, succeeded, hasFailures bool) {
	r.mu.Lock()
	callbacks, ok := r.batchCallbacks[batchID]
	if ok {
		callbacks.progress = nil
		if succeeded && !hasFailures {
			callbacks.catch = nil
		}
		if !succeeded {
			callbacks.then = nil
		}
		if callbacks.progress == nil && callbacks.then == nil && callbacks.catch == nil && callbacks.finally == nil {
			delete(r.batchCallbacks, batchID)
		} else {
			r.batchCallbacks[batchID] = callbacks
		}
	}
	r.mu.Unlock()
}

// finishBatchCallback clears only the closure that ran so concurrently scheduled terminal callbacks remain available.
func (r *runtime) finishBatchCallback(batchID, kind string) {
	r.mu.Lock()
	callbacks, ok := r.batchCallbacks[batchID]
	if ok {
		switch kind {
		case "then":
			callbacks.then = nil
		case "catch":
			callbacks.catch = nil
		case "finally":
			callbacks.finally = nil
		}
		if callbacks.progress == nil && callbacks.then == nil && callbacks.catch == nil && callbacks.finally == nil {
			delete(r.batchCallbacks, batchID)
		} else {
			r.batchCallbacks[batchID] = callbacks
		}
	}
	r.mu.Unlock()
}

// cleanupBatchCallbacks removes terminal workflow entries that have no remaining configured closure.
func (r *runtime) cleanupBatchCallbacks(batchID string) {
	r.mu.Lock()
	callbacks, ok := r.batchCallbacks[batchID]
	if ok && callbacks.progress == nil && callbacks.then == nil && callbacks.catch == nil && callbacks.finally == nil {
		delete(r.batchCallbacks, batchID)
	}
	r.mu.Unlock()
}

// dispatchBatchTerminal publishes one aggregate terminal outcome regardless of which job finishes last.
func (r *runtime) dispatchBatchTerminal(ctx context.Context, env envelope, st BatchState) {
	succeeded := st.Completed && !st.Cancelled
	if succeeded {
		r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: batchFactID(EventBatchCompleted, env), Kind: EventBatchCompleted, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
	}
	r.dispatchBatchTerminalCallbacks(ctx, env, st)
}

// dispatchBatchTerminalCallbacks retries only idempotently claimed callbacks
// when aggregate state committed before their earlier enqueue completed.
func (r *runtime) dispatchBatchTerminalCallbacks(ctx context.Context, env envelope, st BatchState) {
	succeeded := st.Completed && !st.Cancelled
	r.prepareBatchTerminalCallbacks(env.BatchID, succeeded, st.Failed > 0)
	if succeeded {
		_ = r.dispatchCallback(ctx, env, "batch_then", nil)
	}
	_ = r.dispatchCallback(ctx, env, "batch_finally", nil)
	r.cleanupBatchCallbacks(env.BatchID)
}

// batchFactID includes the retained queue-row job identity because the
// established aggregate store persists member outcome but not member payload.
func batchFactID(kind EventKind, env envelope) string {
	return stableWorkflowFactID(kind, env.DispatchID, env.BatchID, env.JobID, env.Job.Type, storedJobEventKey(env.Job), env.Job.Options.Queue)
}

// settleBatchJob uses first-writer outcome ownership when the store supports
// it and preserves the established compatibility path for custom stores.
func (r *runtime) settleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error, claim transitionClaim) (batchSettlementResult, error) {
	if store, ok := r.store.(batchSettlementStore); ok {
		return store.settleBatchOutcome(ctx, batchID, jobID, outcome, cause, claim)
	}
	if store, ok := r.store.(outcomeStore); ok {
		state, owned, err := store.SettleBatchJob(ctx, batchID, jobID, outcome, cause)
		return batchSettlementResult{state: state, owned: owned, claimedNow: true}, err
	}
	switch outcome {
	case BatchJobSucceeded:
		state, _, err := r.store.MarkBatchJobSucceeded(ctx, batchID, jobID)
		return batchSettlementResult{state: state, owned: true, claimedNow: true}, err
	case BatchJobFailed:
		state, _, err := r.store.MarkBatchJobFailed(ctx, batchID, jobID, cause)
		return batchSettlementResult{state: state, owned: true, claimedNow: true}, err
	default:
		return batchSettlementResult{}, errors.New("unsupported batch job outcome")
	}
}

// batchSettlementOwnsTerminal preserves compatibility for established stores
// while requiring built-in receipt-backed settlements to prove that this exact
// member crossed its parent into the terminal state it now reports.
func batchSettlementOwnsTerminal(settled batchSettlementResult, outcome BatchJobOutcome) bool {
	if !settled.state.Completed {
		return false
	}
	if !settled.receiptKnown {
		return true
	}
	return settled.receipt.supported() &&
		settled.receipt.outcome == outcome &&
		settled.receipt.aggregateCompleted &&
		settled.receipt.aggregateCancelled == settled.state.Cancelled
}

// emitCommittedBatchSuccessFacts publishes the success category already owned
// by one member without coupling fact recovery to application callbacks.
func (r *runtime) emitCommittedBatchSuccessFacts(ctx context.Context, env envelope, outcome storedJobOutcome) {
	committedOutcome := outcome
	committedOutcome.err = nil
	r.emitStoredJobOutcome(ctx, committedOutcome)
	r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: batchFactID(EventBatchProgressed, env), Kind: EventBatchProgressed, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
}

// validateRecoveredBatchState rejects aggregate representations that cannot
// safely support reconstruction of a persisted member success.
func validateRecoveredBatchState(env envelope, state BatchState) error {
	if state.BatchID != env.BatchID {
		return fmt.Errorf("requested batch %q returned state for %q", env.BatchID, state.BatchID)
	}
	if state.DispatchID != "" && env.DispatchID != "" && state.DispatchID != env.DispatchID {
		return fmt.Errorf("batch %q dispatch mismatch", env.BatchID)
	}
	if state.Total <= 0 || state.Pending < 0 || state.Processed < 0 || state.Failed < 0 || state.Pending+state.Processed != state.Total || state.Failed > state.Processed {
		return fmt.Errorf("batch %q has inconsistent counters", env.BatchID)
	}
	if !state.Completed && state.Pending == 0 {
		return fmt.Errorf("batch %q exhausted pending members without completing", env.BatchID)
	}
	if state.Completed && !state.Cancelled && state.Pending != 0 {
		return fmt.Errorf("batch %q completed with pending members", env.BatchID)
	}
	return nil
}

// validateRecoveredBatchReceiptShape rejects terminal ownership that cannot
// have been produced atomically with the aggregate state it now describes.
func validateRecoveredBatchReceiptShape(state BatchState, receipt transitionReceipt) error {
	if receipt.aggregateCancelled && !receipt.aggregateCompleted {
		return errors.New("batch transition receipt cancellation is not completed")
	}
	if receipt.aggregateCancelled && receipt.outcome != BatchJobFailed {
		return errors.New("batch transition receipt cancellation does not own failure")
	}
	if !receipt.aggregateCompleted {
		return nil
	}
	if !state.Completed {
		return errors.New("batch transition receipt owns completion for nonterminal state")
	}
	if receipt.aggregateCancelled != state.Cancelled {
		return errors.New("batch transition receipt cancellation does not match aggregate state")
	}
	return nil
}

// recoverCommittedBatchTransition handles a receipt-backed settled member before
// application code runs. Aggregate completion is reconstructed only when a
// separate receipt identifies this member as the transaction that completed it.
func (r *runtime) recoverCommittedBatchTransition(ctx context.Context, env envelope) (bool, error) {
	provenance, recovering := recoveredDeliveryProvenance(ctx)
	if !recovering {
		return false, nil
	}
	state, stateErr := r.store.GetBatch(ctx, env.BatchID)
	if stateErr != nil {
		return true, uncommittedMutationError("recover committed batch transition", stateErr)
	}
	if err := validateRecoveredBatchState(env, state); err != nil {
		return true, uncommittedMutationError("recover committed batch transition", err)
	}
	receiptStore, capable := r.store.(transitionReceiptStore)
	if !capable {
		return false, nil
	}
	receipt, receiptKnown, receiptErr := receiptStore.batchTransitionReceipt(ctx, env.BatchID, env.JobID)
	if receiptErr != nil {
		return true, uncommittedMutationError("recover committed batch transition", receiptErr)
	}
	if !receiptKnown {
		return false, nil
	}
	if receipt.workflowKind != batchTransitionKind || receipt.workflowID != env.BatchID || receipt.memberID != env.JobID || receipt.workflowDispatchID != state.DispatchID || !receipt.workflowCreatedAt.Equal(state.CreatedAt) {
		return true, uncommittedMutationError("recover committed batch transition", errors.New("transition receipt does not match batch state"))
	}
	if err := validateRecoveredTransitionReceipt(env, receipt, true); err != nil {
		return true, uncommittedMutationError("recover committed batch transition", err)
	}
	if err := validateRecoveredBatchReceiptShape(state, receipt); err != nil {
		return true, uncommittedMutationError("recover committed batch transition", err)
	}
	exactFactOwner := transitionReceiptOwnsRecoveredFacts(env, receipt, provenance)
	var settlementErr error
	switch receipt.outcome {
	case BatchJobSucceeded:
		if !exactFactOwner {
			return true, nil
		}
	case BatchJobFailed:
		// The receipt intentionally omits application error details, but its
		// terminal classification must survive every physical redelivery.
		settlementErr = busruntime.Permanent(fmt.Errorf("batch %q member %q was already committed as failed; original cause was not persisted", env.BatchID, env.JobID))
	default:
		return true, uncommittedMutationError("recover committed batch transition", fmt.Errorf("unsupported transition receipt outcome %q", receipt.outcome))
	}
	if receipt.outcome == BatchJobSucceeded {
		committedOutcome, recoveryErr := recoveredStoredJobSuccess(storedJobOutcome{env: env}, receipt, r.now())
		if recoveryErr != nil {
			return true, uncommittedMutationError("recover committed batch transition", recoveryErr)
		}
		r.emitCommittedBatchSuccessFacts(ctx, env, committedOutcome)
	}
	if exactFactOwner && receipt.aggregateCompleted && !receipt.aggregateCancelled && state.Pending == 0 && state.Completed && !state.Cancelled {
		r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: batchFactID(EventBatchCompleted, env), Kind: EventBatchCompleted, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
	}
	return true, settlementErr
}

// handleInternalBatchJob records each batch mutation before publishing its corresponding workflow fact.
func (r *runtime) handleInternalBatchJob(ctx context.Context, job busruntime.InboundJob) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	if _, recovering := recoveredDeliveryProvenance(ctx); recovering {
		applyDeliveryAttempt(ctx, &env)
		handled, recoveryErr := r.recoverCommittedBatchTransition(ctx, env)
		if recoveryErr != nil || handled {
			return recoveryErr
		}
	}
	progress := r.batchProgressCallback(env.BatchID)
	if markErr := r.store.MarkBatchJobStarted(ctx, env.BatchID, env.JobID); markErr != nil {
		return uncommittedMutationError("mark batch job started", markErr)
	}

	outcome := r.executeStoredJobAttempt(ctx, env)
	switch busruntime.ClassifyAttempt(outcome.attempt, outcome.err) {
	case busruntime.AttemptRetry, busruntime.AttemptRedeliver:
		return outcome.err
	case busruntime.AttemptFailed:
		settled, markErr := r.settleBatchJob(ctx, env.BatchID, env.JobID, BatchJobFailed, outcome.err, transitionClaimFromOutcome(ctx, outcome))
		if markErr != nil {
			return uncommittedMutationError("mark batch job failed", markErr)
		}
		markDeliveryTransitionCommitted(ctx, settled.claimedNow, settled.receiptKnown)
		if !settled.owned {
			if _, recovered := recoveredDeliveryProvenance(ctx); !recovered {
				return nil
			}
			_, recoveryErr := r.recoverCommittedBatchTransition(ctx, outcome.env)
			return recoveryErr
		}
		ownsTerminal := batchSettlementOwnsTerminal(settled, BatchJobFailed)
		if !settled.claimedNow {
			if _, recovered := recoveredDeliveryProvenance(ctx); recovered {
				return nil
			}
			if settled.state.Failed == 1 {
				_ = r.dispatchCallback(ctx, env, "batch_catch", outcome.err)
			}
			if ownsTerminal {
				r.dispatchBatchTerminalCallbacks(ctx, env, settled.state)
			}
			return nil
		}
		st := settled.state
		r.emitStoredJobOutcome(ctx, outcome)
		r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: batchFactID(EventBatchProgressed, env), Kind: EventBatchProgressed, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now(), Err: outcome.err})
		if ownsTerminal && st.Cancelled {
			r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventBatchFailed, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now(), Err: outcome.err})
			r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventBatchCancelled, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
		}
		if st.Failed == 1 {
			_ = r.dispatchCallback(ctx, env, "batch_catch", outcome.err)
		}
		r.invokeBatchProgress(ctx, st, progress)
		if ownsTerminal {
			r.dispatchBatchTerminal(ctx, env, st)
		}
		return outcome.err
	}
	settled, markErr := r.settleBatchJob(ctx, env.BatchID, env.JobID, BatchJobSucceeded, nil, transitionClaimFromOutcome(ctx, outcome))
	if markErr != nil {
		return uncommittedMutationError("mark batch job succeeded", markErr)
	}
	markDeliveryTransitionCommitted(ctx, settled.claimedNow, settled.receiptKnown)
	if !settled.owned {
		return nil
	}
	ownsTerminal := batchSettlementOwnsTerminal(settled, BatchJobSucceeded)
	if !settled.claimedNow {
		if _, recovered := recoveredDeliveryProvenance(ctx); recovered {
			_, recoveryErr := r.recoverCommittedBatchTransition(ctx, outcome.env)
			return recoveryErr
		}
		if ownsTerminal {
			r.dispatchBatchTerminalCallbacks(ctx, env, settled.state)
		}
		return nil
	}
	st := settled.state
	r.emitCommittedBatchSuccessFacts(ctx, env, outcome)
	r.invokeBatchProgress(ctx, st, progress)
	if ownsTerminal {
		r.dispatchBatchTerminal(ctx, env, st)
	}
	return nil
}

// batchProgressCallback snapshots an in-flight job's hook before another completion can prepare terminal callbacks.
func (r *runtime) batchProgressCallback(batchID string) func(context.Context, BatchState) error {
	r.mu.RLock()
	progress := r.batchCallbacks[batchID].progress
	r.mu.RUnlock()
	return progress
}

// invokeBatchProgress runs the snapshotted ephemeral progress hook without treating it as durable state.
func (r *runtime) invokeBatchProgress(ctx context.Context, st BatchState, progress func(context.Context, BatchState) error) {
	if progress != nil {
		_ = runEphemeralCallback(func() error { return progress(ctx, st) })
	}
}

// invokeBatchThen claims the successful terminal callback before application code can run.
func (r *runtime) invokeBatchThen(ctx context.Context, st BatchState) error {
	return r.invokeBatchThenObserved(ctx, st, nil)
}

// invokeBatchThenObserved emits lifecycle start only after state validation and idempotency claim succeed.
func (r *runtime) invokeBatchThenObserved(ctx context.Context, st BatchState, onClaimed func()) error {
	if !st.Completed || st.Cancelled {
		return errCallbackNotReady
	}
	key := "batch_then:" + st.BatchID
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
	cb := r.batchCallbacks[st.BatchID]
	r.mu.RUnlock()
	if cb.then != nil {
		defer r.finishBatchCallback(st.BatchID, "then")
		return runEphemeralCallback(func() error { return cb.then(ctx, st) })
	}
	return errCallbackUnavailable
}

// invokeBatchCatch claims the failure callback before application code can run.
func (r *runtime) invokeBatchCatch(ctx context.Context, st BatchState, err error) error {
	return r.invokeBatchCatchObserved(ctx, st, err, nil)
}

// invokeBatchCatchObserved emits lifecycle start only after state validation and idempotency claim succeed.
func (r *runtime) invokeBatchCatchObserved(ctx context.Context, st BatchState, err error, onClaimed func()) error {
	if st.Failed <= 0 && !st.Cancelled {
		return errCallbackNotReady
	}
	key := "batch_catch:" + st.BatchID
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
	cb := r.batchCallbacks[st.BatchID]
	r.mu.RUnlock()
	if cb.catch != nil {
		defer r.finishBatchCallback(st.BatchID, "catch")
		return runEphemeralCallback(func() error { return cb.catch(ctx, st, err) })
	}
	return errCallbackUnavailable
}

// invokeBatchFinally claims the terminal closure before application code can run.
func (r *runtime) invokeBatchFinally(ctx context.Context, st BatchState) error {
	return r.invokeBatchFinallyObserved(ctx, st, nil)
}

// invokeBatchFinallyObserved emits lifecycle start only after state validation and idempotency claim succeed.
func (r *runtime) invokeBatchFinallyObserved(ctx context.Context, st BatchState, onClaimed func()) error {
	if !st.Completed {
		return errCallbackNotReady
	}
	key := "batch_finally:" + st.BatchID
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
	cb := r.batchCallbacks[st.BatchID]
	r.mu.RUnlock()
	if cb.finally == nil {
		return errCallbackUnavailable
	}
	defer r.finishBatchCallback(st.BatchID, "finally")
	return runEphemeralCallback(func() error { return cb.finally(ctx, st) })
}

// callbackOnce persists callback idempotency before invoking application code.
func (r *runtime) callbackOnce(ctx context.Context, key string) (bool, error) {
	marked, err := r.store.MarkCallbackInvoked(ctx, key)
	if err != nil {
		return false, uncommittedMutationError("mark callback invoked", err)
	}
	return marked, nil
}

// handleInternalCallback separates application callback failures from uncommitted store access.
func (r *runtime) handleInternalCallback(ctx context.Context, job busruntime.InboundJob) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	return r.handleCallbackEnvelope(ctx, env)
}

// handleCallbackEnvelope validates, claims, invokes, and observes one decoded callback delivery.
func (r *runtime) handleCallbackEnvelope(ctx context.Context, env envelope) error {
	cbErr := error(nil)
	if env.Error != "" {
		cbErr = errors.New(env.Error)
	}
	start := r.now()
	onClaimed := func() {
		start = r.now()
		r.emit(ctx, Event{
			SchemaVersion: eventSchemaVersion,
			EventID:       newID("evt"),
			Kind:          EventCallbackStarted,
			DispatchID:    env.DispatchID,
			JobID:         env.JobID,
			ChainID:       env.ChainID,
			BatchID:       env.BatchID,
			JobType:       env.Job.Type,
			JobKey:        storedJobEventKey(env.Job),
			Queue:         env.Job.Options.Queue,
			Time:          start,
		})
	}
	var err error
	switch env.CallbackKind {
	case "chain_catch":
		if env.ChainID == "" {
			err = errors.New("chain callback requires chain_id")
			break
		}
		st, stErr := r.store.GetChain(ctx, env.ChainID)
		if stErr != nil {
			err = uncommittedMutationError("read chain callback state", stErr)
			break
		}
		err = r.invokeChainCatchObserved(ctx, st, cbErr, onClaimed)
	case "chain_finally":
		if env.ChainID == "" {
			err = errors.New("chain callback requires chain_id")
			break
		}
		st, stErr := r.store.GetChain(ctx, env.ChainID)
		if stErr != nil {
			err = uncommittedMutationError("read chain callback state", stErr)
			break
		}
		err = r.invokeChainFinallyObserved(ctx, st, onClaimed)
	case "batch_catch":
		if env.BatchID == "" {
			err = errors.New("batch callback requires batch_id")
			break
		}
		st, stErr := r.store.GetBatch(ctx, env.BatchID)
		if stErr != nil {
			err = uncommittedMutationError("read batch callback state", stErr)
			break
		}
		err = r.invokeBatchCatchObserved(ctx, st, cbErr, onClaimed)
	case "batch_then":
		if env.BatchID == "" {
			err = errors.New("batch callback requires batch_id")
			break
		}
		st, stErr := r.store.GetBatch(ctx, env.BatchID)
		if stErr != nil {
			err = uncommittedMutationError("read batch callback state", stErr)
			break
		}
		err = r.invokeBatchThenObserved(ctx, st, onClaimed)
	case "batch_finally":
		if env.BatchID == "" {
			err = errors.New("batch callback requires batch_id")
			break
		}
		st, stErr := r.store.GetBatch(ctx, env.BatchID)
		if stErr != nil {
			err = uncommittedMutationError("read batch callback state", stErr)
			break
		}
		err = r.invokeBatchFinallyObserved(ctx, st, onClaimed)
	default:
		err = errors.New("unknown callback kind")
	}
	if err != nil {
		if errors.Is(err, errCallbackAlreadyInvoked) {
			return nil
		}
		if busruntime.IsUncommitted(err) {
			return err
		}
		r.emit(ctx, Event{
			SchemaVersion: eventSchemaVersion,
			EventID:       newID("evt"),
			Kind:          EventCallbackFailed,
			DispatchID:    env.DispatchID,
			JobID:         env.JobID,
			ChainID:       env.ChainID,
			BatchID:       env.BatchID,
			JobType:       env.Job.Type,
			JobKey:        storedJobEventKey(env.Job),
			Queue:         env.Job.Options.Queue,
			Duration:      r.now().Sub(start),
			Time:          r.now(),
			Err:           err,
		})
		return err
	}
	r.emit(ctx, Event{
		SchemaVersion: eventSchemaVersion,
		EventID:       newID("evt"),
		Kind:          EventCallbackSucceeded,
		DispatchID:    env.DispatchID,
		JobID:         env.JobID,
		ChainID:       env.ChainID,
		BatchID:       env.BatchID,
		JobType:       env.Job.Type,
		JobKey:        storedJobEventKey(env.Job),
		Queue:         env.Job.Options.Queue,
		Duration:      r.now().Sub(start),
		Time:          r.now(),
	})
	return nil
}
