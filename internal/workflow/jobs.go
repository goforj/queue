package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/queue/busruntime"
)

// JobConfig supplies execution policy independently of a workflow coordinator.
type JobConfig struct {
	TranslateError func(error) error
	PanicError     func(any) error
	Clock          func() time.Time
	Observer       Observer
	Middlewares    []Middleware
}

// Jobs owns ordinary delivery and the executor shared by portable coordinators.
// Constructing it does not allocate workflow state or install chain/batch routes.
type Jobs struct {
	translateError func(error) error
	panicError     func(any) error
	q              busruntime.Runtime
	now            func() time.Time
	observer       Observer
	handlerMu      sync.RWMutex
	handlers       map[string]Handler
	middlewares    []Middleware
}

// NewJobs installs only the retained ordinary envelope route; canonical routes
// are installed as logical handlers register, independently of coordination.
func NewJobs(q any, cfg JobConfig) (*Jobs, error) {
	qr, err := asRuntime(q)
	if err != nil {
		return nil, err
	}
	r := &Jobs{translateError: cfg.TranslateError, panicError: cfg.PanicError,
		q:           qr,
		now:         time.Now,
		observer:    cfg.Observer,
		handlers:    make(map[string]Handler),
		middlewares: append([]Middleware(nil), cfg.Middlewares...),
	}
	if cfg.Clock != nil {
		r.now = cfg.Clock
	}
	qr.BusRegister(internalJob, r.handleInternalJob)
	return r, nil
}

// Register binds a job type to a handler.
func (r *Jobs) Register(jobType string, handler Handler) {
	if handler == nil {
		return
	}
	r.handlerMu.Lock()
	r.handlers[jobType] = handler
	r.handlerMu.Unlock()

	_, ok := r.q.(busruntime.DirectRuntime)
	if !ok || IsDeliveryType(jobType) {
		return
	}
	r.q.BusRegister(jobType, func(ctx context.Context, job busruntime.InboundJob) error {
		return r.handleDirectJob(ctx, jobType, job)
	})
}

// Dispatch enqueues one job for execution.
func (r *Jobs) Dispatch(ctx context.Context, job Job) (DispatchResult, error) {
	stored, err := toStoredJob(job)
	if err != nil {
		return DispatchResult{}, err
	}
	return r.dispatch(ctx, stored, false)
}

// DispatchDirect enqueues one ordinary job using its application type and payload.
func (r *Jobs) DispatchDirect(ctx context.Context, job StoredJob) (DispatchResult, error) {
	if job.Type == "" {
		return DispatchResult{}, errors.New("bus job type is required")
	}
	job.Payload = append([]byte(nil), job.Payload...)
	return r.dispatch(ctx, job, true)
}

// dispatch applies one receipt and event contract to both canonical direct
// delivery and the retained legacy envelope route.
func (r *Jobs) dispatch(ctx context.Context, job StoredJob, direct bool) (DispatchResult, error) {
	dispatchID := newID("dsp")
	env := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		Kind:          "job",
		JobID:         newID("job"),
		Job:           job,
	}
	r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventDispatchStarted, DispatchID: dispatchID, JobID: env.JobID, JobType: job.Type, JobKey: storedJobEventKey(job), Queue: job.Options.Queue, Time: r.now()})
	dispatch := func() error { return r.dispatchEnvelope(ctx, internalJob, env) }
	if direct {
		dispatch = func() error { return r.dispatchDirectEnvelope(ctx, env) }
	}
	if err := dispatch(); err != nil {
		if executionErr, ok := acceptedDispatchExecutionError(err); ok {
			r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventDispatchSucceeded, DispatchID: dispatchID, JobID: env.JobID, JobType: job.Type, JobKey: storedJobEventKey(job), Queue: job.Options.Queue, Time: r.now()})
			return DispatchResult{DispatchID: dispatchID}, executionErr
		}
		r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventDispatchFailed, DispatchID: dispatchID, JobID: env.JobID, JobType: job.Type, JobKey: storedJobEventKey(job), Queue: job.Options.Queue, Time: r.now(), Err: err})
		return DispatchResult{DispatchID: dispatchID}, err
	}
	r.emit(ctx, Event{SchemaVersion: eventSchemaVersion, EventID: newID("evt"), Kind: EventDispatchSucceeded, DispatchID: dispatchID, JobID: env.JobID, JobType: job.Type, JobKey: storedJobEventKey(job), Queue: job.Options.Queue, Time: r.now()})
	return DispatchResult{DispatchID: dispatchID}, nil
}

type acceptedDispatchError interface {
	error
	DispatchAccepted() bool
	Unwrap() error
}

// acceptedDispatchExecutionError separates synchronous execution failure from enqueue rejection without coupling workflow to root types.
func acceptedDispatchExecutionError(err error) (error, bool) {
	var accepted acceptedDispatchError
	if !errors.As(err, &accepted) || !accepted.DispatchAccepted() {
		return nil, false
	}
	return accepted.Unwrap(), true
}

// StartWorkers starts the underlying queue worker runtime.
func (r *Jobs) StartWorkers(ctx context.Context) error { return r.q.StartWorkers(ctx) }

// Shutdown stops the underlying queue worker runtime.
func (r *Jobs) Shutdown(ctx context.Context) error { return r.q.Shutdown(ctx) }

// dispatchEnvelope serializes one workflow delivery onto the underlying queue runtime.
func (r *Jobs) dispatchEnvelope(ctx context.Context, jobType string, env envelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return r.q.BusDispatch(ctx, jobType, payload, busruntime.JobOptions{
		Queue:     env.Job.Options.Queue,
		Delay:     env.Job.Options.Delay,
		Timeout:   env.Job.Options.Timeout,
		Retry:     env.Job.Options.Retry,
		Backoff:   env.Job.Options.Backoff,
		UniqueFor: env.Job.Options.UniqueFor,
	})
}

// dispatchDirectEnvelope carries correlation through the driver metadata plane.
// Runtimes without that capability and reserved legacy names keep the frozen
// version-one envelope route.
func (r *Jobs) dispatchDirectEnvelope(ctx context.Context, env envelope) error {
	direct, ok := r.q.(busruntime.DirectRuntime)
	if !ok || IsDeliveryType(env.Job.Type) {
		return r.dispatchEnvelope(ctx, internalJob, env)
	}
	return direct.BusDispatchDirect(ctx, env.Job.Type, env.Job.Payload, busruntime.DeliveryMetadata{
		SchemaVersion: busruntime.DeliveryMetadataVersion,
		DispatchID:    env.DispatchID,
		JobID:         env.JobID,
		ChainID:       env.ChainID,
		BatchID:       env.BatchID,
		Queue:         env.Job.Options.Queue,
	}, busruntime.JobOptions{
		Queue:     env.Job.Options.Queue,
		Delay:     env.Job.Options.Delay,
		Timeout:   env.Job.Options.Timeout,
		Retry:     env.Job.Options.Retry,
		Backoff:   env.Job.Options.Backoff,
		UniqueFor: env.Job.Options.UniqueFor,
	})
}

// handleInternalJob decodes and executes one direct workflow delivery.
func (r *Jobs) handleInternalJob(ctx context.Context, job busruntime.InboundJob) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	return r.executeStoredJob(ctx, env)
}

// handleDirectJob reconstructs engine context from driver metadata while
// leaving the application's type and payload untouched.
func (r *Jobs) handleDirectJob(ctx context.Context, jobType string, job busruntime.InboundJob) error {
	metadata, _ := busruntime.DeliveryMetadataFromContext(ctx)
	attempt, _ := busruntime.DeliveryAttemptFromContext(ctx)
	return r.executeStoredJob(ctx, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    metadata.DispatchID,
		Kind:          "job",
		JobID:         metadata.JobID,
		ChainID:       metadata.ChainID,
		BatchID:       metadata.BatchID,
		Attempt:       attempt.Number,
		Job: StoredJob{
			Type:    jobType,
			Payload: job.PayloadBytes(),
			Options: JobOptions{
				Queue: metadata.Queue,
				Retry: attempt.MaxRetry,
			},
		},
	})
}

// storedJobOutcome carries an attempt result until its owning workflow mutation commits.
type storedJobOutcome struct {
	env      envelope
	attempt  busruntime.DeliveryAttempt
	started  time.Time
	finished time.Time
	err      error
}

// executeStoredJob preserves direct-job behavior while allowing workflows to defer terminal facts until their state commits.
func (r *Jobs) executeStoredJob(ctx context.Context, env envelope) error {
	outcome := r.executeStoredJobAttempt(ctx, env)
	r.emitStoredJobOutcome(ctx, outcome)
	return outcome.err
}

// executeStoredJobAttempt runs one logical handler attempt without claiming its terminal workflow state committed.
func (r *Jobs) executeStoredJobAttempt(ctx context.Context, env envelope) storedJobOutcome {
	attempt := applyDeliveryAttempt(ctx, &env)
	started := r.now()
	r.emit(ctx, Event{
		SchemaVersion: eventSchemaVersion,
		EventID:       newID("evt"),
		Kind:          EventJobStarted,
		DispatchID:    env.DispatchID,
		JobID:         env.JobID,
		ChainID:       env.ChainID,
		BatchID:       env.BatchID,
		Attempt:       env.Attempt,
		JobType:       env.Job.Type,
		JobKey:        storedJobEventKey(env.Job),
		Queue:         env.Job.Options.Queue,
		Time:          started,
	})
	handler, ok := r.lookupHandler(env.Job.Type)
	if !ok {
		err := r.translate(missingHandlerError{jobType: env.Job.Type})
		return storedJobOutcome{env: env, attempt: attempt, started: started, finished: r.now(), err: err}
	}
	jc := NewContext(
		schemaVersion,
		env.DispatchID,
		env.JobID,
		env.ChainID,
		env.BatchID,
		env.Attempt,
		env.Job.Type,
		env.Job.Payload,
	)
	if metadata, ok := busruntime.DeliveryMetadataFromContext(ctx); ok && metadata.Continuation != nil {
		jc = jc.WithContinuation(*metadata.Continuation)
	}
	err := r.invokeHandler(ctx, jc, handler)
	return storedJobOutcome{env: env, attempt: attempt, started: started, finished: r.now(), err: err}
}

// emitStoredJobOutcome publishes only terminal logical facts selected by the shared attempt classifier.
func (r *Jobs) emitStoredJobOutcome(ctx context.Context, outcome storedJobOutcome) {
	kind := EventJobSucceeded
	if outcome.err != nil {
		if busruntime.ClassifyAttempt(outcome.attempt, outcome.err) != busruntime.AttemptFailed {
			return
		}
		kind = EventJobFailed
	}
	r.emit(ctx, Event{
		SchemaVersion: eventSchemaVersion,
		EventID:       storedJobOutcomeFactID(kind, outcome),
		Kind:          kind,
		DispatchID:    outcome.env.DispatchID,
		JobID:         outcome.env.JobID,
		ChainID:       outcome.env.ChainID,
		BatchID:       outcome.env.BatchID,
		Attempt:       outcome.env.Attempt,
		JobType:       outcome.env.Job.Type,
		JobKey:        storedJobEventKey(outcome.env.Job),
		Queue:         outcome.env.Job.Options.Queue,
		Duration:      outcome.finished.Sub(outcome.started),
		Time:          outcome.finished,
		Err:           outcome.err,
	})
}

// middlewareSnapshot isolates an execution attempt from concurrent middleware registration.
func (r *Jobs) middlewareSnapshot() []Middleware {
	r.handlerMu.RLock()
	defer r.handlerMu.RUnlock()
	out := make([]Middleware, len(r.middlewares))
	copy(out, r.middlewares)
	return out
}

// lookupHandler resolves a handler without retaining the registration lock during application execution.
func (r *Jobs) lookupHandler(jobType string) (Handler, bool) {
	r.handlerMu.RLock()
	defer r.handlerMu.RUnlock()
	handler, ok := r.handlers[jobType]
	return handler, ok
}

// emit delays positive workflow facts when the physical driver owns a later settlement boundary.
func (r *Jobs) emit(ctx context.Context, event Event) {
	if eventWaitsForDeliverySettlement(event.Kind) && busruntime.DeferUntilDeliveryCommitted(ctx, func() {
		safeObserve(ctx, r.observer, event)
	}) {
		return
	}
	safeObserve(ctx, r.observer, event)
}

// ErrHandlerNotRegistered identifies an absent logical handler without changing its established error text.
var ErrHandlerNotRegistered = errors.New("workflow handler is not registered")

// missingHandlerError preserves compatibility text while supporting driver-specific terminal classification.
type missingHandlerError struct{ jobType string }

// Error retains the established diagnostic for existing queue consumers.
func (e missingHandlerError) Error() string {
	return fmt.Sprintf("bus handler not registered for %q", e.jobType)
}

// Is exposes missing logical registration independently of its user-selected name.
func (e missingHandlerError) Is(target error) bool { return target == ErrHandlerNotRegistered }

// translate applies only the selected driver's explicit error policy before observer classification.
func (r *Jobs) translate(err error) error {
	if r.translateError != nil {
		return r.translateError(err)
	}
	return err
}

// invokeHandler optionally translates generic queue panics without changing native workflow panic policy.
func (r *Jobs) invokeHandler(ctx context.Context, message Context, handler Handler) (err error) {
	if r.panicError != nil {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = r.translate(r.panicError(recovered))
			}
		}()
	}
	return r.translate(chainMiddleware(r.middlewareSnapshot(), func(ctx context.Context, message Context) error { return handler(ctx, message) })(ctx, message))
}

// ExecuteDirect shares missing-handler execution and facts with driver-owned generic activity wrappers.
func (r *Jobs) ExecuteDirect(ctx context.Context, job StoredJob, metadata busruntime.DeliveryMetadata, attempt busruntime.DeliveryAttempt) error {
	ctx = busruntime.WithDeliveryMetadata(ctx, metadata)
	ctx = busruntime.WithDeliveryAttempt(ctx, attempt)
	return r.executeStoredJob(ctx, envelope{SchemaVersion: schemaVersion, DispatchID: metadata.DispatchID, JobID: metadata.JobID, ChainID: metadata.ChainID, BatchID: metadata.BatchID, Attempt: attempt.Number, Kind: "job", Job: job})
}
