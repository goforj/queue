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

	"github.com/goforj/queue"
)

const (
	schemaVersion = 1

	internalTaskJob       = "bus:job"
	internalTaskChainNode = "bus:chain:node"
	internalTaskBatchJob  = "bus:batch:job"
	internalTaskCallback  = "bus:callback"
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
func New(q queue.Queue, opts ...Option) (Bus, error) {
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
func NewWithStore(q queue.Queue, store Store, opts ...Option) (Bus, error) {
	if q == nil {
		return nil, errors.New("queue is required")
	}
	if store == nil {
		store = NewMemoryStore()
	}
	r := &runtime{
		q:              q,
		store:          store,
		now:            time.Now,
		handlers:       make(map[string]Handler),
		chainCallbacks: make(map[string]chainCallbacks),
		batchCallbacks: make(map[string]batchCallbacks),
	}
	for _, opt := range opts {
		opt(r)
	}

	q.Register(internalTaskJob, r.handleInternalJob)
	q.Register(internalTaskChainNode, r.handleInternalChainNode)
	q.Register(internalTaskBatchJob, r.handleInternalBatchJob)
	q.Register(internalTaskCallback, r.handleInternalCallback)
	return r, nil
}

type runtime struct {
	q     queue.Queue
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
	r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchStarted, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, Queue: wj.Options.Queue, Time: r.now()})
	if err := r.dispatchEnvelope(ctx, internalTaskJob, env); err != nil {
		r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchFailed, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, Queue: wj.Options.Queue, Time: r.now(), Err: err})
		return DispatchResult{DispatchID: dispatchID}, err
	}
	r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventDispatchSucceeded, DispatchID: dispatchID, JobID: env.JobID, JobType: wj.Type, Queue: wj.Options.Queue, Time: r.now()})
	return DispatchResult{DispatchID: dispatchID}, nil
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

func (r *runtime) dispatchEnvelope(ctx context.Context, taskType string, env envelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	job := queue.NewJob(taskType).Payload(payload)
	if env.Job.Options.Queue != "" {
		job = job.OnQueue(env.Job.Options.Queue)
	}
	if env.Job.Options.Delay > 0 {
		job = job.Delay(env.Job.Options.Delay)
	}
	if env.Job.Options.Timeout > 0 {
		job = job.Timeout(env.Job.Options.Timeout)
	}
	if env.Job.Options.Retry > 0 {
		job = job.Retry(env.Job.Options.Retry)
	}
	if env.Job.Options.Backoff > 0 {
		job = job.Backoff(env.Job.Options.Backoff)
	}
	if env.Job.Options.UniqueFor > 0 {
		job = job.UniqueFor(env.Job.Options.UniqueFor)
	}
	return r.q.DispatchCtx(ctx, job)
}

func (r *runtime) dispatchCallback(ctx context.Context, base envelope, kind string, err error) error {
	cbEnv := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    base.DispatchID,
		Kind:          "callback",
		JobID:         newID("job"),
		ChainID:       base.ChainID,
		BatchID:       base.BatchID,
		CallbackKind:  kind,
		Job: wireJob{
			Options: JobOptions{
				Queue: base.Job.Options.Queue,
			},
		},
	}
	if err != nil {
		cbEnv.Error = err.Error()
	}
	return r.dispatchEnvelope(ctx, internalTaskCallback, cbEnv)
}

func (r *runtime) handleInternalJob(ctx context.Context, task queue.Job) error {
	var env envelope
	if err := task.Bind(&env); err != nil {
		return err
	}
	return r.executeWireJob(ctx, env)
}

func (r *runtime) executeWireJob(ctx context.Context, env envelope) error {
	start := r.now()
	r.emit(Event{
		SchemaVersion: schemaVersion,
		EventID:       newID("evt"),
		Kind:          EventJobStarted,
		DispatchID:    env.DispatchID,
		JobID:         env.JobID,
		ChainID:       env.ChainID,
		BatchID:       env.BatchID,
		Attempt:       env.Attempt,
		JobType:       env.Job.Type,
		Queue:         env.Job.Options.Queue,
		Time:          start,
	})
	handler, ok := r.lookupHandler(env.Job.Type)
	if !ok {
		err := fmt.Errorf("bus handler not registered for %q", env.Job.Type)
		r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventJobFailed, DispatchID: env.DispatchID, JobID: env.JobID, ChainID: env.ChainID, BatchID: env.BatchID, Attempt: env.Attempt, JobType: env.Job.Type, Queue: env.Job.Options.Queue, Duration: r.now().Sub(start), Time: r.now(), Err: err})
		return err
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
	if err != nil {
		r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventJobFailed, DispatchID: env.DispatchID, JobID: env.JobID, ChainID: env.ChainID, BatchID: env.BatchID, Attempt: env.Attempt, JobType: env.Job.Type, Queue: env.Job.Options.Queue, Duration: r.now().Sub(start), Time: r.now(), Err: err})
		return err
	}
	r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventJobSucceeded, DispatchID: env.DispatchID, JobID: env.JobID, ChainID: env.ChainID, BatchID: env.BatchID, Attempt: env.Attempt, JobType: env.Job.Type, Queue: env.Job.Options.Queue, Duration: r.now().Sub(start), Time: r.now()})
	return nil
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

func (r *runtime) emit(event Event) {
	safeObserve(r.observer, event)
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
