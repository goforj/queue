package bus

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
	"github.com/goforj/queue/internal/workflow"
)

const (
	// schemaVersion keeps the existing private name while the protocol becomes engine-owned.
	schemaVersion = workflow.ProtocolSchemaVersion
	// internalJob keeps the existing direct-delivery name stable on the wire.
	internalJob = workflow.DirectDeliveryType
	// internalJobChainNode keeps the existing chain-delivery name stable on the wire.
	internalJobChainNode = workflow.ChainNodeDeliveryType
	// internalJobBatchJob keeps the existing batch-delivery name stable on the wire.
	internalJobBatchJob = workflow.BatchJobDeliveryType
	// internalJobCallback keeps the existing callback-delivery name stable on the wire.
	internalJobCallback = workflow.CallbackDeliveryType
)

type Bus interface {
	Register(jobType string, handler Handler)

	Dispatch(ctx context.Context, job Job) (DispatchResult, error)
	Chain(jobs ...Job) ChainBuilder
	Batch(jobs ...Job) BatchBuilder

	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error

	FindBatch(ctx context.Context, batchID string) (BatchState, error)
	FindChain(ctx context.Context, chainID string) (ChainState, error)
	Prune(ctx context.Context, before time.Time) error
}

type Option func(*runtime)

// WithObserver installs an event observer for dispatch/job/chain/batch lifecycle hooks.
// @group Options
//
// Example: attach observer
//
//	observer := bus.ObserverFunc(func(event bus.Event) {
//		_ = event.Kind
//	})
//	b, _ := bus.New(q, bus.WithObserver(observer))
//	_ = b
func WithObserver(observer Observer) Option {
	return func(r *runtime) {
		r.observer = observer
	}
}

// WithStore overrides the orchestration store used for chain/batch/callback state.
// @group Options
//
// Example: custom store
//
//	store := bus.NewMemoryStore()
//	b, _ := bus.New(q, bus.WithStore(store))
//	_ = b
func WithStore(store Store) Option {
	return func(r *runtime) {
		if store != nil {
			r.store = store
		}
	}
}

// WithClock overrides the runtime clock used for event/state timestamps.
// @group Options
//
// Example: fixed clock
//
//	fixed := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
//	b, _ := bus.New(q, bus.WithClock(func() time.Time { return fixed }))
//	_ = b
func WithClock(clock func() time.Time) Option {
	return func(r *runtime) {
		if clock != nil {
			r.now = clock
		}
	}
}

// WithMiddleware appends middleware to the runtime execution chain.
// @group Options
//
// Example: add middleware
//
//	audit := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
//		return next(ctx, jc)
//	})
//	skipHealth := bus.SkipWhen{
//		Predicate: func(_ context.Context, jc bus.Context) bool { return jc.JobType == "health:ping" },
//	}
//	fatalize := bus.FailOnError{
//		When: func(err error) bool { return err != nil },
//	}
//	b, _ := bus.New(q, bus.WithMiddleware(audit, skipHealth, fatalize))
//	_ = b
func WithMiddleware(middlewares ...Middleware) Option {
	return func(r *runtime) {
		for _, m := range middlewares {
			if m != nil {
				r.middlewares = append(r.middlewares, m)
			}
		}
	}
}

// New creates a bus runtime using an in-memory orchestration store.
// @group Constructors
//
// Example: new bus runtime
//
//	q, _ := queue.NewSync()
//	b, _ := bus.New(q)
//	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
//	_ = b.StartWorkers(context.Background())
//	defer b.Shutdown(context.Background())
//	type PollPayload struct {
//		URL string `json:"url"`
//	}
//	_, _ = b.Dispatch(context.Background(), bus.NewJob("monitor:poll", PollPayload{
//		URL: "https://goforj.dev/health",
//	}))
func New(q any, opts ...Option) (Bus, error) {
	return NewWithStore(q, NewMemoryStore(), opts...)
}

// NewWithStore creates a bus runtime with a custom orchestration store.
// @group Constructors
//
// Example: new bus with store
//
//	q, _ := queue.NewSync()
//	store := bus.NewMemoryStore()
//	b, _ := bus.NewWithStore(q, store)
//	_ = b
func NewWithStore(q any, store Store, opts ...Option) (Bus, error) {
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
		opt(r)
	}

	qr.BusRegister(internalJob, r.handleInternalJob)
	qr.BusRegister(internalJobChainNode, r.handleInternalChainNode)
	qr.BusRegister(internalJobBatchJob, r.handleInternalBatchJob)
	qr.BusRegister(internalJobCallback, r.handleInternalCallback)
	return r, nil
}

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

var _ Bus = (*runtime)(nil)

// Register binds a job type to a handler.
// @group Runtime
//
// Example: register handler
//
//	b.Register("emails:send", func(ctx context.Context, jc bus.Context) error { return nil })
func (r *runtime) Register(jobType string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[jobType] = handler
}

// Dispatch enqueues one job for execution.
// @group Runtime
//
// Example: dispatch one job
//
//	_, _ = b.Dispatch(context.Background(), bus.NewJob("emails:send", map[string]any{"id": 1}))
func (r *runtime) Dispatch(ctx context.Context, job Job) (DispatchResult, error) {
	wj, err := toWireJob(job)
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
	r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchStarted, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: wireJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now()})
	if err := r.dispatchEnvelope(ctx, internalJob, env); err != nil {
		if executionErr, ok := acceptedDispatchExecutionError(err); ok {
			r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchSucceeded, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: wireJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now()})
			return DispatchResult{DispatchID: dispatchID}, executionErr
		}
		r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchFailed, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: wireJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now(), Err: err})
		return DispatchResult{DispatchID: dispatchID}, err
	}
	r.emit(ctx, Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchSucceeded, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, JobKey: wireJobEventKey(wj), Queue: wj.Options.Queue, Time: r.now()})
	return DispatchResult{DispatchID: dispatchID}, nil
}

type acceptedDispatchError interface {
	error
	DispatchAccepted() bool
	Unwrap() error
}

// acceptedDispatchExecutionError separates synchronous execution failure from enqueue rejection without coupling bus to root types.
func acceptedDispatchExecutionError(err error) (error, bool) {
	var accepted acceptedDispatchError
	if !errors.As(err, &accepted) || !accepted.DispatchAccepted() {
		return nil, false
	}
	return accepted.Unwrap(), true
}

// Chain creates a sequential workflow where each job runs only after the prior job succeeds.
// @group Chaining
//
// Example: dispatch chain
//
//	type PollPayload struct {
//		URL string `json:"url"`
//	}
//	type DownsamplePayload struct {
//		Window string `json:"window"`
//	}
//	type AlertPayload struct {
//		Severity string `json:"severity"`
//	}
//	chainID, _ := b.Chain(
//		bus.NewJob("monitor:poll", PollPayload{URL: "https://goforj.dev/health"}),
//		bus.NewJob("monitor:downsample", DownsamplePayload{Window: "5m"}),
//		bus.NewJob("monitor:alert", AlertPayload{Severity: "critical"}),
//	).OnQueue("monitor-critical").
//		Catch(func(context.Context, bus.ChainState, error) error { return nil }).
//		Finally(func(context.Context, bus.ChainState) error { return nil }).
//		Dispatch(context.Background())
//	_ = chainID
func (r *runtime) Chain(jobs ...Job) ChainBuilder {
	return &chainBuilder{r: r, jobs: append([]Job(nil), jobs...)}
}

// Batch creates a parallel workflow and tracks aggregate completion state.
// @group Batching
//
// Example: dispatch batch
//
//	type PollPayload struct {
//		URL string `json:"url"`
//	}
//	batchID, _ := b.Batch(
//		bus.NewJob("monitor:poll", PollPayload{URL: "https://a"}),
//		bus.NewJob("monitor:poll", PollPayload{URL: "https://b"}),
//	).Name("monitor sweep").
//		OnQueue("monitor-scan").
//		AllowFailures().
//		Progress(func(context.Context, bus.BatchState) error { return nil }).
//		Then(func(context.Context, bus.BatchState) error { return nil }).
//		Catch(func(context.Context, bus.BatchState, error) error { return nil }).
//		Finally(func(context.Context, bus.BatchState) error { return nil }).
//		Dispatch(context.Background())
//	_ = batchID
func (r *runtime) Batch(jobs ...Job) BatchBuilder {
	return &batchBuilder{r: r, jobs: append([]Job(nil), jobs...)}
}

// StartWorkers starts the underlying queue worker runtime.
// @group Runtime
//
// Example: start workers
//
//	_ = b.StartWorkers(context.Background())
func (r *runtime) StartWorkers(ctx context.Context) error { return r.q.StartWorkers(ctx) }

// Shutdown stops the underlying queue worker runtime.
// @group Runtime
//
// Example: shutdown workers
//
//	_ = b.Shutdown(context.Background())
func (r *runtime) Shutdown(ctx context.Context) error { return r.q.Shutdown(ctx) }

// FindBatch returns persisted batch state by id.
// @group Runtime
//
// Example: find batch state
//
//	st, _ := b.FindBatch(context.Background(), "bat_123")
//	_ = st
func (r *runtime) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	return r.store.GetBatch(ctx, batchID)
}

// FindChain returns persisted chain state by id.
// @group Runtime
//
// Example: find chain state
//
//	st, _ := b.FindChain(context.Background(), "chn_123")
//	_ = st
func (r *runtime) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	return r.store.GetChain(ctx, chainID)
}

// Prune removes terminal orchestration records older than before.
// @group Runtime
//
// Example: prune old state
//
//	_ = b.Prune(context.Background(), time.Now().Add(-24*time.Hour))
func (r *runtime) Prune(ctx context.Context, before time.Time) error {
	return r.store.Prune(ctx, before)
}

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
		Job: wireJob{
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

func (r *runtime) handleInternalJob(ctx context.Context, job busruntime.InboundJob) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	return r.executeWireJob(ctx, env)
}

// wireJobOutcome carries an attempt result until its owning workflow mutation commits.
type wireJobOutcome struct {
	env      envelope
	attempt  busruntime.DeliveryAttempt
	started  time.Time
	finished time.Time
	err      error
}

// executeWireJob preserves direct-job behavior while allowing workflows to defer terminal facts until their state commits.
func (r *runtime) executeWireJob(ctx context.Context, env envelope) error {
	outcome := r.executeWireJobAttempt(ctx, env)
	r.emitWireJobOutcome(ctx, outcome)
	return outcome.err
}

// executeWireJobAttempt runs one logical handler attempt without claiming its terminal workflow state committed.
func (r *runtime) executeWireJobAttempt(ctx context.Context, env envelope) wireJobOutcome {
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
		JobKey:        wireJobEventKey(env.Job),
		Queue:         env.Job.Options.Queue,
		Time:          started,
	})
	handler, ok := r.lookupHandler(env.Job.Type)
	if !ok {
		err := fmt.Errorf("bus handler not registered for %q", env.Job.Type)
		return wireJobOutcome{env: env, attempt: attempt, started: started, finished: r.now(), err: err}
	}
	jc := Context{
		SchemaVersion: schemaVersion,
		DispatchID:    env.DispatchID,
		JobID:         env.JobID,
		ChainID:       env.ChainID,
		BatchID:       env.BatchID,
		Attempt:       env.Attempt,
		JobType:       env.Job.Type,
		payload:       append([]byte(nil), env.Job.Payload...),
	}
	err := chainMiddleware(r.middlewareSnapshot(), func(ctx context.Context, c Context) error {
		return handler(ctx, c)
	})(ctx, jc)
	return wireJobOutcome{env: env, attempt: attempt, started: started, finished: r.now(), err: err}
}

// emitWireJobOutcome publishes only terminal logical facts selected by the shared attempt classifier.
func (r *runtime) emitWireJobOutcome(ctx context.Context, outcome wireJobOutcome) {
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
		JobKey:        wireJobEventKey(outcome.env.Job),
		Queue:         outcome.env.Job.Options.Queue,
		Duration:      outcome.finished.Sub(outcome.started),
		Time:          outcome.finished,
		Err:           outcome.err,
	})
}

// wireJobEventKey keeps workflow facts on the same logical type-and-payload correlation as queue and worker facts.
func wireJobEventKey(job wireJob) string {
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

func (r *runtime) middlewareSnapshot() []Middleware {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Middleware, len(r.middlewares))
	copy(out, r.middlewares)
	return out
}

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

type wireJob struct {
	Type    string     `json:"type"`
	Payload []byte     `json:"payload"`
	Options JobOptions `json:"options"`
}

func toWireJob(job Job) (wireJob, error) {
	if job.Type == "" {
		return wireJob{}, errors.New("bus job type is required")
	}
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return wireJob{}, err
	}
	return wireJob{
		Type:    job.Type,
		Payload: payload,
		Options: job.Options,
	}, nil
}

type envelope struct {
	SchemaVersion int     `json:"schema_version"`
	DispatchID    string  `json:"dispatch_id"`
	Kind          string  `json:"kind"`
	JobID         string  `json:"job_id"`
	ChainID       string  `json:"chain_id,omitempty"`
	BatchID       string  `json:"batch_id,omitempty"`
	NodeID        string  `json:"node_id,omitempty"`
	Attempt       int     `json:"attempt"`
	Job           wireJob `json:"job"`
	CallbackKind  string  `json:"callback_kind,omitempty"`
	Error         string  `json:"error,omitempty"`
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
