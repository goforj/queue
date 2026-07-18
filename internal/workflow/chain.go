package workflow

import (
	"context"
	"errors"
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
	b.r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainStarted, DispatchID: dispatchID, ChainID: chainID, JobType: first.Job.Type, JobKey: storedJobEventKey(first.Job), Queue: first.Job.Options.Queue, Time: b.r.now()})
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
		if st, stErr := b.r.store.GetChain(ctx, chainID); stErr == nil && (st.Failed || st.Completed || st.NextIndex > 0) {
			return chainID, err
		}
		if failErr := b.r.store.FailChain(ctx, chainID, err); failErr != nil {
			return chainID, uncommittedMutationError("fail chain after initial dispatch rejection", errors.Join(err, failErr))
		}
		base := envelope{DispatchID: dispatchID, ChainID: chainID, Job: first.Job}
		b.r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainFailed, DispatchID: dispatchID, ChainID: chainID, JobType: first.Job.Type, JobKey: storedJobEventKey(first.Job), Queue: first.Job.Options.Queue, Time: b.r.now(), Err: err})
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

// handleInternalChainNode advances or fails a chain only after its application attempt reaches a committable outcome.
func (r *runtime) handleInternalChainNode(ctx context.Context, job busruntime.InboundJob) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	outcome := r.executeStoredJobAttempt(ctx, env)
	switch busruntime.ClassifyAttempt(outcome.attempt, outcome.err) {
	case busruntime.AttemptRetry, busruntime.AttemptRedeliver:
		return outcome.err
	case busruntime.AttemptFailed:
		if markErr := r.store.FailChain(ctx, env.ChainID, outcome.err); markErr != nil {
			return uncommittedMutationError("fail chain", markErr)
		}
		r.emitStoredJobOutcome(ctx, outcome)
		r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainFailed, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now(), Err: outcome.err})
		_ = r.dispatchCallback(ctx, env, "chain_catch", outcome.err)
		_ = r.dispatchCallback(ctx, env, "chain_finally", nil)
		r.cleanupChainCallbacks(env.ChainID)
		return outcome.err
	}
	next, done, advErr := r.store.AdvanceChain(ctx, env.ChainID, env.NodeID)
	if advErr != nil {
		return uncommittedMutationError("advance chain", advErr)
	}
	r.emitStoredJobOutcome(ctx, outcome)
	if done {
		r.prepareChainSuccessCallbacks(env.ChainID)
		r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainCompleted, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
		_ = r.dispatchCallback(ctx, env, "chain_finally", nil)
		r.cleanupChainCallbacks(env.ChainID)
		return nil
	}
	r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainAdvanced, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, JobKey: storedJobEventKey(env.Job), Queue: env.Job.Options.Queue, Time: r.now()})
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

// invokeChainCatch claims the ephemeral catch callback before application code can run.
func (r *runtime) invokeChainCatch(ctx context.Context, st ChainState, err error) error {
	return r.invokeChainCatchObserved(ctx, st, err, nil)
}

// invokeChainCatchObserved emits lifecycle start only after state validation and idempotency claim succeed.
func (r *runtime) invokeChainCatchObserved(ctx context.Context, st ChainState, err error, onClaimed func()) error {
	if !st.Failed {
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
