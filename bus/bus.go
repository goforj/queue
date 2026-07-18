package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/workflow"
)

// ErrQueueOptionsUnsupported indicates that bus construction options cannot be
// retrofitted onto an already configured queue.Queue.
var ErrQueueOptionsUnsupported = errors.New("bus options cannot configure an existing queue.Queue")

// Bus is the legacy workflow orchestration contract.
//
// Deprecated: use queue.Queue.
type Bus interface {
	// Register binds a legacy workflow handler to a job type.
	Register(jobType string, handler Handler)
	// Dispatch submits one legacy workflow job.
	Dispatch(ctx context.Context, job Job) (DispatchResult, error)
	// Chain creates a sequential workflow builder.
	Chain(jobs ...Job) ChainBuilder
	// Batch creates an aggregate workflow builder.
	Batch(jobs ...Job) BatchBuilder
	// StartWorkers starts the underlying queue worker runtime.
	StartWorkers(ctx context.Context) error
	// Shutdown stops the underlying queue worker runtime.
	Shutdown(ctx context.Context) error
	// FindBatch returns persisted batch state.
	FindBatch(ctx context.Context, batchID string) (BatchState, error)
	// FindChain returns persisted chain state.
	FindChain(ctx context.Context, chainID string) (ChainState, error)
	// Prune removes terminal workflow state older than the supplied time.
	Prune(ctx context.Context, before time.Time) error
}

// ChainBuilder configures and dispatches a sequential workflow.
//
// Deprecated: use queue.ChainBuilder.
type ChainBuilder interface {
	// OnQueue applies a default queue to chain jobs without an explicit target.
	OnQueue(queue string) ChainBuilder
	// Catch registers the explicitly ephemeral chain failure callback.
	Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder
	// Finally registers the explicitly ephemeral chain terminal callback.
	Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder
	// Dispatch persists and starts the chain workflow.
	Dispatch(ctx context.Context) (string, error)
}

// BatchBuilder configures and dispatches an aggregate workflow.
//
// Deprecated: use queue.BatchBuilder.
type BatchBuilder interface {
	// Name assigns an application-facing label to the batch.
	Name(name string) BatchBuilder
	// OnQueue applies a default queue to batch jobs without an explicit target.
	OnQueue(queue string) BatchBuilder
	// AllowFailures keeps remaining members active after a terminal member failure.
	AllowFailures() BatchBuilder
	// Progress registers the explicitly ephemeral batch progress callback.
	Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	// Then registers the explicitly ephemeral batch success callback.
	Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	// Catch registers the explicitly ephemeral batch failure callback.
	Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder
	// Finally registers the explicitly ephemeral batch terminal callback.
	Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	// Dispatch persists and starts the batch workflow.
	Dispatch(ctx context.Context) (string, error)
}

// Option configures the legacy raw-runtime construction route.
//
// Deprecated: configure queue.Queue directly. Options are rejected when New
// receives an already constructed queue.Queue because its engine already exists.
type Option func(*optionConfig)

type optionConfig struct {
	observer    Observer
	store       Store
	clock       func() time.Time
	middlewares []Middleware
}

// WithObserver installs a legacy workflow observer on a raw-runtime bus.
//
// Deprecated: use queue.WithObserver.
func WithObserver(observer Observer) Option {
	return func(config *optionConfig) {
		config.observer = observer
	}
}

// WithStore selects the workflow store on a raw-runtime bus.
//
// Deprecated: use queue.WithStore.
func WithStore(store Store) Option {
	return func(config *optionConfig) {
		if store != nil {
			config.store = store
		}
	}
}

// WithClock selects the workflow clock on a raw-runtime bus.
//
// Deprecated: use queue.WithClock.
func WithClock(clock func() time.Time) Option {
	return func(config *optionConfig) {
		if clock != nil {
			config.clock = clock
		}
	}
}

// WithMiddleware appends middleware to a raw-runtime bus.
//
// Deprecated: use queue.WithMiddleware.
func WithMiddleware(middlewares ...Middleware) Option {
	return func(config *optionConfig) {
		for _, middleware := range middlewares {
			if middleware != nil {
				config.middlewares = append(config.middlewares, middleware)
			}
		}
	}
}

// New returns a compatibility view over queue.Queue or constructs the retained
// low-level route when q implements busruntime.Runtime.
//
// Deprecated: construct and use queue.Queue directly.
func New(q any, opts ...Option) (Bus, error) {
	if existing, ok := q.(*queue.Queue); ok {
		if existing == nil {
			return nil, errors.New("queue is required")
		}
		if hasConstructionOptions(opts) {
			return nil, fmt.Errorf("%w: pass options to queue.New instead", ErrQueueOptionsUnsupported)
		}
		return &queueAdapter{queue: existing}, nil
	}
	return newRawRuntimeAdapter(q, nil, opts...)
}

// NewWithStore constructs the retained low-level bus route with an explicit
// store. An existing queue.Queue must instead receive queue.WithStore when built.
//
// Deprecated: use queue.New with queue.WithStore.
func NewWithStore(q any, store Store, opts ...Option) (Bus, error) {
	if existing, ok := q.(*queue.Queue); ok {
		if existing == nil {
			return nil, errors.New("queue is required")
		}
		return nil, fmt.Errorf("%w: pass queue.WithStore to queue.New instead", ErrQueueOptionsUnsupported)
	}
	return newRawRuntimeAdapter(q, store, opts...)
}

// hasConstructionOptions distinguishes an option-free facade request from an
// attempt to mutate an engine that queue.Queue has already configured.
func hasConstructionOptions(opts []Option) bool {
	for _, option := range opts {
		if option != nil {
			return true
		}
	}
	return false
}

// newRawRuntimeAdapter preserves the advanced busruntime.Runtime construction
// seam while delegating all orchestration behavior to the single internal engine.
func newRawRuntimeAdapter(q any, store Store, opts ...Option) (Bus, error) {
	config := optionConfig{store: store}
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	engineOptions := make([]workflow.Option, 0, 3)
	if config.observer != nil {
		engineOptions = append(engineOptions, workflow.WithObserver(legacyObserverAdapter{observer: config.observer}))
	}
	if config.clock != nil {
		engineOptions = append(engineOptions, workflow.WithClock(config.clock))
	}
	if len(config.middlewares) > 0 {
		engineOptions = append(engineOptions, workflow.WithMiddleware(toWorkflowMiddlewares(config.middlewares)...))
	}
	engine, err := workflow.NewWithStore(q, toWorkflowStore(config.store), engineOptions...)
	if err != nil {
		return nil, err
	}
	return &runtimeAdapter{engine: engine}, nil
}

type legacyObserverAdapter struct {
	observer Observer
}

// Observe translates the canonical engine event into the frozen legacy event shape.
func (a legacyObserverAdapter) Observe(ctx context.Context, event workflow.Event) {
	safeObserve(ctx, a.observer, Event{
		SchemaVersion: event.SchemaVersion,
		EventID:       event.EventID,
		Kind:          EventKind(event.Kind),
		DispatchID:    event.DispatchID,
		JobID:         event.JobID,
		ChainID:       event.ChainID,
		BatchID:       event.BatchID,
		Attempt:       event.Attempt,
		JobType:       event.JobType,
		JobKey:        event.JobKey,
		Queue:         event.Queue,
		Duration:      event.Duration,
		Time:          event.Time,
		Err:           event.Err,
	})
}

type runtimeAdapter struct {
	engine workflow.Engine
}

var _ Bus = (*runtimeAdapter)(nil)

// Register adapts a legacy handler to the internal engine message contract.
func (a *runtimeAdapter) Register(jobType string, handler Handler) {
	if handler == nil {
		a.engine.Register(jobType, nil)
		return
	}
	a.engine.Register(jobType, func(ctx context.Context, message workflow.Context) error {
		return handler(ctx, toQueueMessage(message))
	})
}

// Dispatch converts the legacy boundary DTO without changing when payload JSON is encoded.
func (a *runtimeAdapter) Dispatch(ctx context.Context, job Job) (DispatchResult, error) {
	result, err := a.engine.Dispatch(ctx, toWorkflowJob(job))
	return toQueueDispatchResult(result), err
}

// Chain converts legacy job DTOs and wraps the engine's self-returning builder interface.
func (a *runtimeAdapter) Chain(jobs ...Job) ChainBuilder {
	converted := make([]workflow.Job, 0, len(jobs))
	for _, job := range jobs {
		converted = append(converted, toWorkflowJob(job))
	}
	return &runtimeChainBuilder{inner: a.engine.Chain(converted...)}
}

// Batch converts legacy job DTOs and wraps the engine's self-returning builder interface.
func (a *runtimeAdapter) Batch(jobs ...Job) BatchBuilder {
	converted := make([]workflow.Job, 0, len(jobs))
	for _, job := range jobs {
		converted = append(converted, toWorkflowJob(job))
	}
	return &runtimeBatchBuilder{inner: a.engine.Batch(converted...)}
}

// StartWorkers forwards worker startup to the raw runtime engine.
func (a *runtimeAdapter) StartWorkers(ctx context.Context) error {
	return a.engine.StartWorkers(ctx)
}

// Shutdown forwards worker shutdown to the raw runtime engine.
func (a *runtimeAdapter) Shutdown(ctx context.Context) error {
	return a.engine.Shutdown(ctx)
}

// FindBatch forwards persisted batch lookup to the internal engine.
func (a *runtimeAdapter) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	state, err := a.engine.FindBatch(ctx, batchID)
	return toQueueBatchState(state), err
}

// FindChain forwards persisted chain lookup to the internal engine.
func (a *runtimeAdapter) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	state, err := a.engine.FindChain(ctx, chainID)
	return toQueueChainState(state), err
}

// Prune forwards workflow retention to the internal engine.
func (a *runtimeAdapter) Prune(ctx context.Context, before time.Time) error {
	return a.engine.Prune(ctx, before)
}

// toWorkflowJob maps the legacy mutable DTO into the engine model without
// encoding Payload, preserving the legacy Dispatch-time failure boundary.
func toWorkflowJob(job Job) workflow.Job {
	return workflow.Job{
		Type:    job.Type,
		Payload: job.Payload,
		Options: workflow.JobOptions{
			Queue:     job.Options.Queue,
			Delay:     job.Options.Delay,
			Timeout:   job.Options.Timeout,
			Retry:     job.Options.Retry,
			Backoff:   job.Options.Backoff,
			UniqueFor: job.Options.UniqueFor,
		},
	}
}

type runtimeChainBuilder struct {
	inner workflow.ChainBuilder
}

// OnQueue applies a default queue to jobs without an explicit target.
func (b *runtimeChainBuilder) OnQueue(queueName string) ChainBuilder {
	b.inner = b.inner.OnQueue(queueName)
	return b
}

// Catch registers the legacy failure callback on the internal builder.
func (b *runtimeChainBuilder) Catch(callback func(context.Context, ChainState, error) error) ChainBuilder {
	if callback == nil {
		b.inner = b.inner.Catch(nil)
		return b
	}
	b.inner = b.inner.Catch(func(ctx context.Context, state workflow.ChainState, err error) error {
		return callback(ctx, toQueueChainState(state), err)
	})
	return b
}

// Finally registers the legacy terminal callback on the internal builder.
func (b *runtimeChainBuilder) Finally(callback func(context.Context, ChainState) error) ChainBuilder {
	if callback == nil {
		b.inner = b.inner.Finally(nil)
		return b
	}
	b.inner = b.inner.Finally(func(ctx context.Context, state workflow.ChainState) error {
		return callback(ctx, toQueueChainState(state))
	})
	return b
}

// Dispatch creates and starts the internal chain workflow.
func (b *runtimeChainBuilder) Dispatch(ctx context.Context) (string, error) {
	return b.inner.Dispatch(ctx)
}

type runtimeBatchBuilder struct {
	inner workflow.BatchBuilder
}

// Name sets the display name on the internal batch builder.
func (b *runtimeBatchBuilder) Name(name string) BatchBuilder {
	b.inner = b.inner.Name(name)
	return b
}

// OnQueue applies a default queue to jobs without an explicit target.
func (b *runtimeBatchBuilder) OnQueue(queueName string) BatchBuilder {
	b.inner = b.inner.OnQueue(queueName)
	return b
}

// AllowFailures keeps remaining batch jobs active after one member fails.
func (b *runtimeBatchBuilder) AllowFailures() BatchBuilder {
	b.inner = b.inner.AllowFailures()
	return b
}

// Progress registers the legacy progress callback on the internal builder.
func (b *runtimeBatchBuilder) Progress(callback func(context.Context, BatchState) error) BatchBuilder {
	if callback == nil {
		b.inner = b.inner.Progress(nil)
		return b
	}
	b.inner = b.inner.Progress(func(ctx context.Context, state workflow.BatchState) error {
		return callback(ctx, toQueueBatchState(state))
	})
	return b
}

// Then registers the legacy success callback on the internal builder.
func (b *runtimeBatchBuilder) Then(callback func(context.Context, BatchState) error) BatchBuilder {
	if callback == nil {
		b.inner = b.inner.Then(nil)
		return b
	}
	b.inner = b.inner.Then(func(ctx context.Context, state workflow.BatchState) error {
		return callback(ctx, toQueueBatchState(state))
	})
	return b
}

// Catch registers the legacy failure callback on the internal builder.
func (b *runtimeBatchBuilder) Catch(callback func(context.Context, BatchState, error) error) BatchBuilder {
	if callback == nil {
		b.inner = b.inner.Catch(nil)
		return b
	}
	b.inner = b.inner.Catch(func(ctx context.Context, state workflow.BatchState, err error) error {
		return callback(ctx, toQueueBatchState(state), err)
	})
	return b
}

// Finally registers the legacy terminal callback on the internal builder.
func (b *runtimeBatchBuilder) Finally(callback func(context.Context, BatchState) error) BatchBuilder {
	if callback == nil {
		b.inner = b.inner.Finally(nil)
		return b
	}
	b.inner = b.inner.Finally(func(ctx context.Context, state workflow.BatchState) error {
		return callback(ctx, toQueueBatchState(state))
	})
	return b
}

// Dispatch creates and starts the internal batch workflow.
func (b *runtimeBatchBuilder) Dispatch(ctx context.Context) (string, error) {
	return b.inner.Dispatch(ctx)
}

type queueAdapter struct {
	queue *queue.Queue
}

var _ Bus = (*queueAdapter)(nil)

// Register forwards a legacy handler to the already configured root queue.
func (a *queueAdapter) Register(jobType string, handler Handler) {
	a.queue.Register(jobType, handler)
}

// Dispatch binds ctx to the root queue for this call and preserves legacy payload JSON semantics.
func (a *queueAdapter) Dispatch(ctx context.Context, job Job) (DispatchResult, error) {
	converted, err := toQueueJob(job)
	if err != nil {
		return DispatchResult{}, err
	}
	return a.queue.WithContext(ctx).Dispatch(converted)
}

// Chain snapshots legacy job values while preserving Dispatch-time payload encoding.
func (a *queueAdapter) Chain(jobs ...Job) ChainBuilder {
	return &queueChainBuilder{
		queue: a.queue,
		jobs:  append([]Job(nil), jobs...),
	}
}

// Batch snapshots legacy job values while preserving Dispatch-time payload encoding.
func (a *queueAdapter) Batch(jobs ...Job) BatchBuilder {
	return &queueBatchBuilder{
		queue: a.queue,
		jobs:  append([]Job(nil), jobs...),
	}
}

// StartWorkers starts the existing root queue runtime.
func (a *queueAdapter) StartWorkers(ctx context.Context) error {
	return a.queue.StartWorkers(ctx)
}

// Shutdown stops the existing root queue runtime.
func (a *queueAdapter) Shutdown(ctx context.Context) error {
	return a.queue.Shutdown(ctx)
}

// FindBatch reads batch state from the root queue's configured store.
func (a *queueAdapter) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	return a.queue.FindBatch(ctx, batchID)
}

// FindChain reads chain state from the root queue's configured store.
func (a *queueAdapter) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	return a.queue.FindChain(ctx, chainID)
}

// Prune applies retention through the root queue's configured store.
func (a *queueAdapter) Prune(ctx context.Context, before time.Time) error {
	return a.queue.Prune(ctx, before)
}

type queueChainBuilder struct {
	queue     *queue.Queue
	jobs      []Job
	queueName string
	catch     func(context.Context, ChainState, error) error
	finally   func(context.Context, ChainState) error
}

// OnQueue forwards queue selection while retaining the legacy fluent return type.
func (b *queueChainBuilder) OnQueue(queueName string) ChainBuilder {
	b.queueName = queueName
	return b
}

// Catch forwards the legacy failure callback to the root builder.
func (b *queueChainBuilder) Catch(callback func(context.Context, ChainState, error) error) ChainBuilder {
	b.catch = callback
	return b
}

// Finally forwards the legacy terminal callback to the root builder.
func (b *queueChainBuilder) Finally(callback func(context.Context, ChainState) error) ChainBuilder {
	b.finally = callback
	return b
}

// Dispatch converts the shallow legacy job snapshot at the historical dispatch boundary.
func (b *queueChainBuilder) Dispatch(ctx context.Context) (string, error) {
	converted, err := toQueueJobs(b.jobs)
	if err != nil {
		return "", err
	}
	return b.queue.Chain(converted...).
		OnQueue(b.queueName).
		Catch(b.catch).
		Finally(b.finally).
		Dispatch(ctx)
}

type queueBatchBuilder struct {
	queue         *queue.Queue
	jobs          []Job
	name          string
	queueName     string
	allowFailures bool
	progress      func(context.Context, BatchState) error
	then          func(context.Context, BatchState) error
	catch         func(context.Context, BatchState, error) error
	finally       func(context.Context, BatchState) error
}

// Name forwards the application-facing batch label while retaining the legacy fluent return type.
func (b *queueBatchBuilder) Name(name string) BatchBuilder {
	b.name = name
	return b
}

// OnQueue forwards queue selection while retaining the legacy fluent return type.
func (b *queueBatchBuilder) OnQueue(queueName string) BatchBuilder {
	b.queueName = queueName
	return b
}

// AllowFailures forwards fail-soft behavior while retaining the legacy fluent return type.
func (b *queueBatchBuilder) AllowFailures() BatchBuilder {
	b.allowFailures = true
	return b
}

// Progress forwards the legacy progress callback to the root builder.
func (b *queueBatchBuilder) Progress(callback func(context.Context, BatchState) error) BatchBuilder {
	b.progress = callback
	return b
}

// Then forwards the legacy success callback to the root builder.
func (b *queueBatchBuilder) Then(callback func(context.Context, BatchState) error) BatchBuilder {
	b.then = callback
	return b
}

// Catch forwards the legacy failure callback to the root builder.
func (b *queueBatchBuilder) Catch(callback func(context.Context, BatchState, error) error) BatchBuilder {
	b.catch = callback
	return b
}

// Finally forwards the legacy terminal callback to the root builder.
func (b *queueBatchBuilder) Finally(callback func(context.Context, BatchState) error) BatchBuilder {
	b.finally = callback
	return b
}

// Dispatch converts the shallow legacy job snapshot at the historical dispatch boundary.
func (b *queueBatchBuilder) Dispatch(ctx context.Context) (string, error) {
	converted, err := toQueueJobs(b.jobs)
	if err != nil {
		return "", err
	}
	builder := b.queue.Batch(converted...).
		Name(b.name).
		OnQueue(b.queueName)
	if b.allowFailures {
		builder = builder.AllowFailures()
	}
	return builder.
		Progress(b.progress).
		Then(b.then).
		Catch(b.catch).
		Finally(b.finally).
		Dispatch(ctx)
}

// toQueueJobs converts a legacy workflow job slice while retaining the first conversion error.
func toQueueJobs(jobs []Job) ([]queue.Job, error) {
	converted := make([]queue.Job, 0, len(jobs))
	for _, job := range jobs {
		convertedJob, err := toQueueJob(job)
		if err != nil {
			return nil, err
		}
		converted = append(converted, convertedJob)
	}
	return converted, nil
}

// toQueueJob freezes the legacy DTO's json.Marshal result as raw canonical
// payload bytes so strings, byte slices, RawMessage, nil, and custom marshalers
// retain their historical wire representation.
func toQueueJob(job Job) (queue.Job, error) {
	if job.Type == "" {
		return queue.Job{}, errors.New("bus job type is required")
	}
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return queue.Job{}, err
	}
	converted := queue.NewJob(job.Type).Payload(json.RawMessage(payload))
	if job.Options.Queue != "" {
		converted = converted.OnQueue(job.Options.Queue)
	}
	if job.Options.Delay != 0 {
		converted = converted.Delay(job.Options.Delay)
	}
	if job.Options.Timeout != 0 {
		converted = converted.Timeout(job.Options.Timeout)
	}
	converted = converted.Retry(job.Options.Retry)
	if job.Options.Backoff != 0 {
		converted = converted.Backoff(job.Options.Backoff)
	}
	if job.Options.UniqueFor != 0 {
		converted = converted.UniqueFor(job.Options.UniqueFor)
	}
	return converted, nil
}
