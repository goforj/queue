package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/goforj/queue/busruntime"
)

// CoordinatorLifecycle lets the shared runtime host recovery without another worker owner.
type CoordinatorLifecycle interface {
	StartCoordinator(context.Context) error
	StopCoordinator(context.Context) error
	CoordinatorDone() <-chan struct{}
	CoordinatorError() error
}

// continuationRunner publishes eligible durable heads without waiting for global completion.
type continuationRunner struct {
	cancel context.CancelFunc
	done   chan struct{}
	fatal  chan struct{}
	mu     sync.Mutex
	err    error
}

// StartCoordinator starts at most one resource-scoped recovery publisher after physical workers.
func (r *runtime) StartCoordinator(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.coordinatorStopped {
		return errors.New("workflow coordinator is stopping")
	}
	if r.continuationResource == "" {
		return nil
	}
	if r.runner != nil {
		return nil
	}
	store, ok := r.store.(ContinuationStore)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &continuationRunner{cancel: cancel, done: make(chan struct{}), fatal: make(chan struct{})}
	r.runner = runner
	go r.publishContinuations(ctx, runner, store)
	return nil
}

// publishContinuations retries transient store/acceptance failures with bounded backoff.
func (r *runtime) publishContinuations(ctx context.Context, runner *continuationRunner, store ContinuationStore) {
	defer func() {
		if recover() != nil {
			runner.mu.Lock()
			if runner.err == nil {
				runner.err = errors.New("workflow continuation publisher failed")
				close(runner.fatal)
			}
			runner.mu.Unlock()
		}
		close(runner.done)
	}()
	owner := newID("publisher")
	backoff := 20 * time.Millisecond
	for {
		claim, ok, err := store.ClaimContinuation(ctx, r.continuationResource, owner, time.Now(), time.Minute)
		if err == nil && ok {
			payload, encodeErr := json.Marshal(claim)
			if encodeErr != nil {
				runner.mu.Lock()
				runner.err = errors.New("workflow continuation encoding failed")
				close(runner.fatal)
				runner.mu.Unlock()
				return
			}
			job := claim.Intent.Job
			err = r.q.BusDispatch(busruntime.WithIndependentExecution(ctx), ContinuationDeliveryType, payload, busruntime.JobOptions{Queue: job.Options.Queue, Timeout: job.Options.Timeout, Retry: 0})
			// Ambiguous acceptance keeps the lease and invocation identity. Expiry permits recovery.
		}
		if errors.Is(err, ErrContinuationProtocol) || busruntime.IsPermanent(err) {
			runner.mu.Lock()
			runner.err = errors.New("workflow continuation recovery failed")
			close(runner.fatal)
			runner.mu.Unlock()
			return
		}
		if err != nil {
			backoff *= 2
			if backoff > 2*time.Second {
				backoff = 2 * time.Second
			}
		} else {
			backoff = 20 * time.Millisecond
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// StopCoordinator stops new claims and joins only this publisher's in-flight acceptance.
func (r *runtime) StopCoordinator(ctx context.Context) error {
	r.mu.Lock()
	r.coordinatorStopped = true
	runner := r.runner
	r.mu.Unlock()
	if runner == nil {
		return nil
	}
	runner.cancel()
	select {
	case <-runner.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CoordinatorDone returns the fatal channel without treating normal shutdown as failure.
func (r *runtime) CoordinatorDone() <-chan struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.runner == nil {
		return nil
	}
	return r.runner.fatal
}

// CoordinatorError reports a stable recovery-publisher failure for Run and readiness.
func (r *runtime) CoordinatorError() error {
	r.mu.RLock()
	runner := r.runner
	r.mu.RUnlock()
	if runner == nil {
		return nil
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.err
}

// FindContinuations returns committed triggered intents without interpreting hook definitions as pending.
func (r *runtime) FindContinuations(ctx context.Context, id string) ([]busruntime.ContinuationOutcome, error) {
	store, ok := r.store.(ContinuationStore)
	if !ok {
		return nil, errors.New("workflow store does not support durable continuations")
	}
	return store.ContinuationOutcomes(ctx, id)
}

// handleContinuation validates fenced authority before invoking the shared logical handler.
func (r *runtime) handleContinuation(ctx context.Context, job busruntime.InboundJob) error {
	if len(job.PayloadBytes()) > 256<<10 {
		return busruntime.Permanent(errors.New("workflow continuation delivery exceeds byte budget"))
	}
	var claim continuationClaim
	if err := job.Bind(&claim); err != nil {
		return busruntime.Permanent(errors.New("invalid workflow continuation delivery"))
	}
	if claim.Version != 1 || claim.Resource != r.continuationResource {
		return busruntime.Permanent(errors.New("workflow continuation belongs to another resource"))
	}
	store, ok := r.store.(ContinuationStore)
	if !ok {
		return busruntime.Permanent(errors.New("workflow store does not support durable continuations"))
	}
	var execution continuationClaim
	var valid bool
	owner := newID("execution")
	err := retryContinuationStore(ctx, func(attemptCtx context.Context) error {
		var err error
		execution, valid, err = store.BeginContinuation(attemptCtx, claim, owner, time.Now())
		return err
	})
	if err != nil {
		return busruntime.Uncommitted(err)
	}
	if !valid {
		return nil
	}
	claim = execution
	ctx = busruntime.WithDeliveryAttempt(ctx, busruntime.DeliveryAttempt{Number: claim.Attempt, MaxRetry: claim.Intent.Job.Options.Retry})
	renewCtx, cancelRenew := context.WithCancel(context.Background())
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		timer := time.NewTicker(20 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-timer.C:
				attempt, cancel := context.WithTimeout(renewCtx, 10*time.Second)
				_ = store.RenewContinuation(attempt, claim, time.Now())
				cancel()
			}
		}
	}()
	defer func() { cancelRenew(); <-renewDone }()
	invocation := claim.Intent.Outcome.Invocation
	metadata := busruntime.DeliveryMetadata{SchemaVersion: busruntime.DeliveryMetadataVersion, DispatchID: invocation.DispatchID, JobID: invocation.ID, Queue: claim.Intent.Job.Options.Queue, Continuation: &invocation}
	if invocation.Trigger.Kind == "chain" {
		metadata.ChainID = invocation.WorkflowID
	} else {
		metadata.BatchID = invocation.WorkflowID
	}
	ctx = busruntime.WithDeliveryMetadata(ctx, metadata)
	env := envelope{SchemaVersion: schemaVersion, DispatchID: invocation.DispatchID, JobID: invocation.ID, ChainID: metadata.ChainID, BatchID: metadata.BatchID, Kind: "job", Job: claim.Intent.Job}
	outcome := r.executeContinuationAttempt(ctx, env)
	status := "succeeded"
	if outcome.err != nil {
		switch busruntime.ClassifyAttempt(outcome.attempt, outcome.err) {
		case busruntime.AttemptRedeliver:
			status = "redeliver"
		case busruntime.AttemptRetry:
			status = "retry"
		default:
			status = "exhausted"
		}
	}
	settledAt := r.now()
	if status == "retry" || status == "redeliver" {
		// Eligibility shares the publisher's wall clock; the configurable clock remains for terminal state and events.
		settledAt = time.Now()
	}
	if err := retryContinuationStore(context.WithoutCancel(ctx), func(attemptCtx context.Context) error {
		return store.SettleContinuation(attemptCtx, claim, status, settledAt)
	}); err != nil {
		return busruntime.Uncommitted(err)
	}
	r.emitStoredJobOutcome(ctx, outcome)
	return nil
}

// retryContinuationStore retries transient authority operations without repeating an admitted application effect.
func retryContinuationStore(ctx context.Context, operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	backoff := 10 * time.Millisecond
	for {
		err := operation(ctx)
		if err == nil || errors.Is(err, ErrContinuationProtocol) || errors.Is(err, ErrNotFound) || busruntime.IsPermanent(err) {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 250*time.Millisecond {
			backoff *= 2
		}
	}
}

// executeContinuationAttempt contains named-hook panics on framework-owned publisher and worker goroutines.
func (r *runtime) executeContinuationAttempt(ctx context.Context, env envelope) (outcome storedJobOutcome) {
	started := r.now()
	attempt, _ := busruntime.DeliveryAttemptFromContext(ctx)
	env.Attempt = attempt.Number
	defer func() {
		if recover() != nil {
			outcome = storedJobOutcome{env: env, attempt: attempt, started: started, finished: r.now(), err: busruntime.Permanent(errors.New("workflow continuation handler panicked"))}
		}
	}()
	return r.executeStoredJobAttempt(ctx, env)
}
