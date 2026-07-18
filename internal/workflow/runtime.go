package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/jobidentity"
)

const (
	// schemaVersion keeps the existing private name while the protocol becomes engine-owned.
	schemaVersion = ProtocolSchemaVersion
	// internalJob keeps the existing direct-delivery name stable on the wire.
	internalJob = DirectDeliveryType
	// internalJobChainNode keeps the existing chain-delivery name stable on the wire.
	internalJobChainNode = ChainNodeDeliveryType
	// internalJobBatchJob keeps the existing batch-delivery name stable on the wire.
	internalJobBatchJob = BatchJobDeliveryType
	// internalJobCallback keeps the existing callback-delivery name stable on the wire.
	internalJobCallback = CallbackDeliveryType
)

// Engine defines the orchestration surface shared by the public facade and compatibility adapters.
type Engine interface {
	// Register binds a logical job type to its handler.
	Register(jobType string, handler Handler)

	// Dispatch submits one logical job through the underlying delivery runtime.
	Dispatch(ctx context.Context, job Job) (DispatchResult, error)
	// Chain creates a sequential workflow builder.
	Chain(jobs ...Job) ChainBuilder
	// Batch creates an aggregate workflow builder.
	Batch(jobs ...Job) BatchBuilder

	// StartWorkers starts the underlying delivery runtime.
	StartWorkers(ctx context.Context) error
	// Shutdown stops the underlying delivery runtime.
	Shutdown(ctx context.Context) error

	// FindBatch returns persisted aggregate workflow state.
	FindBatch(ctx context.Context, batchID string) (BatchState, error)
	// FindChain returns persisted sequential workflow state.
	FindChain(ctx context.Context, chainID string) (ChainState, error)
	// Prune removes terminal workflow state older than before.
	Prune(ctx context.Context, before time.Time) error
}

// Option configures one workflow runtime before its internal handlers are registered.
type Option func(*runtime)

// WithObserver installs an event observer for workflow lifecycle facts.
func WithObserver(observer Observer) Option {
	return func(r *runtime) {
		r.observer = observer
	}
}

// WithStore overrides the orchestration store used for chain/batch/callback state.
func WithStore(store Store) Option {
	return func(r *runtime) {
		if store != nil {
			r.store = store
		}
	}
}

// WithClock overrides the runtime clock used for event/state timestamps.
func WithClock(clock func() time.Time) Option {
	return func(r *runtime) {
		if clock != nil {
			r.now = clock
		}
	}
}

// WithMiddleware appends middleware to the runtime execution chain.
func WithMiddleware(middlewares ...Middleware) Option {
	return func(r *runtime) {
		for _, m := range middlewares {
			if m != nil {
				r.middlewares = append(r.middlewares, m)
			}
		}
	}
}

// New creates a workflow engine using an in-memory orchestration store.
func New(q any, opts ...Option) (Engine, error) {
	return NewWithStore(q, NewMemoryStore(), opts...)
}

// NewWithStore creates a workflow engine with a custom orchestration store.
func NewWithStore(q any, store Store, opts ...Option) (Engine, error) {
	if q == nil {
		return nil, errors.New("queue is required")
	}
	qr, err := asRuntime(q)
	if err != nil {
		return nil, err
	}
	if store == nil {
		store = NewMemoryStore()
	}
	r := &runtime{
		q:              qr,
		store:          store,
		now:            time.Now,
		handlers:       make(map[string]Handler),
		chainCallbacks: make(map[string]chainCallbacks),
		batchCallbacks: make(map[string]batchCallbacks),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	qr.BusRegister(internalJob, r.handleInternalJob)
	qr.BusRegister(internalJobChainNode, r.handleInternalChainNode)
	qr.BusRegister(internalJobBatchJob, r.handleInternalBatchJob)
	qr.BusRegister(internalJobCallback, r.handleInternalCallback)
	return r, nil
}

// asRuntime narrows compatibility inputs to the transport contract required by workflow orchestration.
func asRuntime(v any) (busruntime.Runtime, error) {
	if v == nil {
		return nil, errors.New("queue is required")
	}
	if q, ok := v.(busruntime.Runtime); ok {
		return q, nil
	}
	return nil, fmt.Errorf("queue does not support bus runtime adapter")
}

type runtime struct {
	q     busruntime.Runtime
	store Store
	now   func() time.Time

	observer Observer

	mu             sync.RWMutex
	handlers       map[string]Handler
	chainCallbacks map[string]chainCallbacks
	batchCallbacks map[string]batchCallbacks
	middlewares    []Middleware
}

var _ Engine = (*runtime)(nil)

// Register binds a job type to a handler.
func (r *runtime) Register(jobType string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[jobType] = handler
}

// Dispatch enqueues one job for execution.
func (r *runtime) Dispatch(ctx context.Context, job Job) (DispatchResult, error) {
	wj, err := toStoredJob(job)
	if err != nil {
		return DispatchResult{}, err
	}
	dispatchID := newID("dsp")
	env := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		Kind:          "job",
		JobID:         newID("job"),
		Job:           wj,
	}
	r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchStarted, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: storedJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now()})
	if err := r.dispatchEnvelope(ctx, internalJob, env); err != nil {
		if executionErr, ok := acceptedDispatchExecutionError(err); ok {
			r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchSucceeded, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: storedJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now()})
			return DispatchResult{DispatchID: dispatchID}, executionErr
		}
		r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchFailed, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: storedJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now(), Err: err})
		return DispatchResult{DispatchID: dispatchID}, err
	}
	r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchSucceeded, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: storedJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now()})
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

// Chain creates a sequential workflow where each job runs only after the prior job succeeds.
func (r *runtime) Chain(jobs ...Job) ChainBuilder {
	return &chainBuilder{r: r, jobs: append([]Job(nil), jobs...)}
}

// Batch creates a parallel workflow and tracks aggregate completion state.
func (r *runtime) Batch(jobs ...Job) BatchBuilder {
	return &batchBuilder{r: r, jobs: append([]Job(nil), jobs...)}
}

// StartWorkers starts the underlying queue worker runtime.
func (r *runtime) StartWorkers(ctx context.Context) error { return r.q.StartWorkers(ctx) }

// Shutdown stops the underlying queue worker runtime.
func (r *runtime) Shutdown(ctx context.Context) error { return r.q.Shutdown(ctx) }

// FindBatch returns persisted batch state by id.
func (r *runtime) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	return r.store.GetBatch(ctx, batchID)
}

// FindChain returns persisted chain state by id.
func (r *runtime) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	return r.store.GetChain(ctx, chainID)
}

// Prune removes terminal orchestration records older than before.
func (r *runtime) Prune(ctx context.Context, before time.Time) error {
	return r.store.Prune(ctx, before)
}

// dispatchEnvelope serializes one workflow delivery onto the underlying queue runtime.
func (r *runtime) dispatchEnvelope(ctx context.Context, jobType string, env envelope) error {
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

// dispatchCallback schedules only configured ephemeral closures through the same queue delivery path.
func (r *runtime) dispatchCallback(ctx context.Context, base envelope, kind string, err error) error {
	callback, ok := r.callbackEnvelope(base, kind, err)
	if !ok {
		return nil
	}
	return r.dispatchEnvelope(ctx, internalJobCallback, callback)
}

// callbackEnvelope constructs one configured ephemeral callback delivery without coupling invocation to its transport.
func (r *runtime) callbackEnvelope(base envelope, kind string, err error) (envelope, bool) {
	if !r.callbackConfigured(base, kind) {
		return envelope{}, false
	}
	callback := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    base.DispatchID,
		Kind:          "callback",
		JobID:         newID("job"),
		ChainID:       base.ChainID,
		BatchID:       base.BatchID,
		CallbackKind:  kind,
		Job: StoredJob{
			Type:    base.Job.Type,
			Payload: append([]byte(nil), base.Job.Payload...),
			Options: JobOptions{
				Queue: base.Job.Options.Queue,
			},
		},
	}
	if err != nil {
		callback.Error = err.Error()
	}
	return callback, true
}

// invokeCallbackInline preserves callback lifecycle semantics when initial workflow enqueue never reaches a worker.
func (r *runtime) invokeCallbackInline(ctx context.Context, base envelope, kind string, err error) error {
	callback, ok := r.callbackEnvelope(base, kind, err)
	if !ok {
		return nil
	}
	return r.handleCallbackEnvelope(ctx, callback)
}

// callbackConfigured keeps absent optional closures from becoming artificial callback deliveries and success facts.
func (r *runtime) callbackConfigured(base envelope, kind string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	switch kind {
	case "chain_catch":
		return r.chainCallbacks[base.ChainID].catch != nil
	case "chain_finally":
		return r.chainCallbacks[base.ChainID].finally != nil
	case "batch_catch":
		return r.batchCallbacks[base.BatchID].catch != nil
	case "batch_then":
		return r.batchCallbacks[base.BatchID].then != nil
	case "batch_finally":
		return r.batchCallbacks[base.BatchID].finally != nil
	default:
		return false
	}
}

// handleInternalJob decodes and executes one direct workflow delivery.
func (r *runtime) handleInternalJob(ctx context.Context, job busruntime.InboundJob) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	return r.executeStoredJob(ctx, env)
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
func (r *runtime) executeStoredJob(ctx context.Context, env envelope) error {
	outcome := r.executeStoredJobAttempt(ctx, env)
	r.emitStoredJobOutcome(ctx, outcome)
	return outcome.err
}

// executeStoredJobAttempt runs one logical handler attempt without claiming its terminal workflow state committed.
func (r *runtime) executeStoredJobAttempt(ctx context.Context, env envelope) storedJobOutcome {
	attempt := applyDeliveryAttempt(ctx, &env)
	started := r.now()
	r.emit(ctx, Event{
		SchemaVersion: schemaVersion,
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
		err := fmt.Errorf("bus handler not registered for %q", env.Job.Type)
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
	err := chainMiddleware(r.middlewareSnapshot(), func(ctx context.Context, c Context) error {
		return handler(ctx, c)
	})(ctx, jc)
	return storedJobOutcome{env: env, attempt: attempt, started: started, finished: r.now(), err: err}
}

// emitStoredJobOutcome publishes only terminal logical facts selected by the shared attempt classifier.
func (r *runtime) emitStoredJobOutcome(ctx context.Context, outcome storedJobOutcome) {
	kind := EventJobSucceeded
	if outcome.err != nil {
		if busruntime.ClassifyAttempt(outcome.attempt, outcome.err) != busruntime.AttemptFailed {
			return
		}
		kind = EventJobFailed
	}
	r.emit(ctx, Event{
		SchemaVersion: schemaVersion,
		EventID:       newID("evt"),
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

// storedJobEventKey keeps workflow facts on the same logical type-and-payload correlation as queue and worker facts.
func storedJobEventKey(job StoredJob) string {
	return jobidentity.ObservedKey(job.Type, job.Payload)
}

// uncommittedMutationError marks state persistence failures for same-attempt infrastructure redelivery.
func uncommittedMutationError(operation string, err error) error {
	return busruntime.Uncommitted(fmt.Errorf("%s: %w", operation, err))
}

// applyDeliveryAttempt replaces the stale envelope attempt with metadata supplied by the physical worker.
func applyDeliveryAttempt(ctx context.Context, env *envelope) busruntime.DeliveryAttempt {
	if attempt, ok := busruntime.DeliveryAttemptFromContext(ctx); ok {
		env.Attempt = attempt.Number
		return attempt
	}
	return busruntime.DeliveryAttempt{
		Number:   env.Attempt,
		MaxRetry: env.Job.Options.Retry,
	}
}

// middlewareSnapshot isolates an execution attempt from concurrent middleware registration.
func (r *runtime) middlewareSnapshot() []Middleware {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Middleware, len(r.middlewares))
	copy(out, r.middlewares)
	return out
}

// lookupHandler resolves a handler without retaining the registration lock during application execution.
func (r *runtime) lookupHandler(jobType string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[jobType]
	return handler, ok
}

// emit delays positive workflow facts when the physical driver owns a later settlement boundary.
func (r *runtime) emit(ctx context.Context, event Event) {
	if eventWaitsForDeliverySettlement(event.Kind) && busruntime.DeferUntilDeliveryCommitted(ctx, func() {
		safeObserve(ctx, r.observer, event)
	}) {
		return
	}
	safeObserve(ctx, r.observer, event)
}

// eventWaitsForDeliverySettlement identifies positive workflow facts that would be false if broker acknowledgement remains unresolved.
func eventWaitsForDeliverySettlement(kind EventKind) bool {
	switch kind {
	case EventJobSucceeded,
		EventChainAdvanced,
		EventChainCompleted,
		EventBatchProgressed,
		EventBatchCompleted,
		EventCallbackSucceeded:
		return true
	default:
		return false
	}
}

// runEphemeralCallback converts application panics into callback failures without unwinding committed workflow state.
func runEphemeralCallback(callback func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok {
				err = fmt.Errorf("workflow callback panicked: %w", recoveredErr)
				return
			}
			err = fmt.Errorf("workflow callback panicked: %v", recovered)
		}
	}()
	return callback()
}

// StoredJob is the version-one logical job representation embedded in workflow state and delivery envelopes.
type StoredJob struct {
	Type    string     `json:"type"`
	Payload []byte     `json:"payload"`
	Options JobOptions `json:"options"`
}

// toStoredJob validates and serializes an application payload into the stable workflow representation.
func toStoredJob(job Job) (StoredJob, error) {
	if job.Type == "" {
		return StoredJob{}, errors.New("bus job type is required")
	}
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return StoredJob{}, err
	}
	return StoredJob{
		Type:    job.Type,
		Payload: payload,
		Options: job.Options,
	}, nil
}

type envelope struct {
	SchemaVersion int       `json:"schema_version"`
	DispatchID    string    `json:"dispatch_id"`
	Kind          string    `json:"kind"`
	JobID         string    `json:"job_id"`
	ChainID       string    `json:"chain_id,omitempty"`
	BatchID       string    `json:"batch_id,omitempty"`
	NodeID        string    `json:"node_id,omitempty"`
	Attempt       int       `json:"attempt"`
	Job           StoredJob `json:"job"`
	CallbackKind  string    `json:"callback_kind,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// newID creates correlation identifiers with the legacy prefix and random hexadecimal shape.
func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
