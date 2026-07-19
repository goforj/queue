package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/workflow"
)

// WorkflowEventKind identifies high-level workflow runtime lifecycle events.
//
// Deprecated: use EventKind. Delivery and workflow facts now share one event model.
// @group Queue
type WorkflowEventKind = EventKind

// WorkflowEvent is emitted by the high-level workflow runtime observer hooks.
//
// Deprecated: use Event. Delivery and workflow facts now share one event model.
// @group Queue
type WorkflowEvent = Event

// WorkflowObserver receives high-level workflow runtime events.
//
// Deprecated: use Observer. A single observer now receives every event layer.
// @group Queue
type WorkflowObserver = Observer

// WorkflowObserverFunc adapts a function to a workflow observer.
//
// Deprecated: use ObserverFunc. A single observer now receives every event layer.
// @group Queue
type WorkflowObserverFunc = ObserverFunc

// Permanent marks an error as terminal so workers do not spend the remaining application retry budget on it.
// @group Queue
func Permanent(err error) error {
	return busruntime.Permanent(err)
}

// IsPermanent reports whether an error requests terminal application settlement.
// @group Queue
func IsPermanent(err error) bool {
	return busruntime.IsPermanent(err)
}

// Option configures the high-level queue and workflow runtime.
// @group Queue
type Option func(*runtimeOptions)

type runtimeOptions struct {
	workflowOpts            []workflow.Option
	workers                 int
	observer                Observer
	handlerContextDecorator func(context.Context) context.Context
	legacyDirectEnvelope    bool
}

// apply ignores nil options so optional configuration slices compose safely.
func (o *runtimeOptions) apply(opts []Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
}

// WithObserver installs one observer for queue, worker, and workflow lifecycle events.
// @group Queue
//
// Example: observe all queue activity
//
//	observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) {
//		_ = event.Kind
//	})
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithObserver(observer))
//	if err != nil {
//		return
//	}
//	_ = q
func WithObserver(observer Observer) Option {
	return func(o *runtimeOptions) {
		if observer == nil {
			return
		}
		if o.observer == nil {
			o.observer = observer
			return
		}
		o.observer = MultiObserver(o.observer, observer)
	}
}

// WithStore overrides the workflow orchestration store.
// @group Queue
//
// Example: workflow store
//
//	var store queue.WorkflowStore
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithStore(store))
//	if err != nil {
//		return
//	}
//	_ = q
func WithStore(store WorkflowStore) Option {
	return func(o *runtimeOptions) {
		o.workflowOpts = append(o.workflowOpts, workflow.WithStore(workflowStoreFromRoot(store)))
	}
}

// WithClock overrides the workflow runtime clock.
// @group Queue
//
// Example: workflow clock
//
//	q, err := queue.New(
//		queue.Config{Driver: queue.DriverSync},
//		queue.WithClock(func() time.Time { return time.Unix(0, 0) }),
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func WithClock(clock func() time.Time) Option {
	return func(o *runtimeOptions) {
		o.workflowOpts = append(o.workflowOpts, workflow.WithClock(clock))
	}
}

// WithMiddleware appends queue workflow middleware.
// @group Queue
//
// Example: middleware
//
//	mw := queue.MiddlewareFunc(func(ctx context.Context, m queue.Message, next queue.Next) error {
//		return next(ctx, m)
//	})
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithMiddleware(mw))
//	if err != nil {
//		return
//	}
//	_ = q
func WithMiddleware(middlewares ...Middleware) Option {
	return func(o *runtimeOptions) {
		o.workflowOpts = append(o.workflowOpts, workflow.WithMiddleware(middlewaresToWorkflow(middlewares)...))
	}
}

// WithWorkers sets desired worker concurrency before StartWorkers.
// It applies to high-level queue constructors (for example NewWorkerpool/New/NewSync).
// @group Queue
//
// Example: constructor workers option
//
//	q, err := queue.NewWorkerpool(
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func WithWorkers(count int) Option {
	return func(o *runtimeOptions) {
		if count <= 0 {
			return
		}
		o.workers = count
	}
}

// WithHandlerContextDecorator decorates queue handler execution context before
// process lifecycle events and handler execution run.
// @group Queue
//
// Example: decorate handler context
//
//	q, err := queue.New(
//		queue.Config{Driver: queue.DriverSync},
//		queue.WithHandlerContextDecorator(func(ctx context.Context) context.Context {
//			return context.WithValue(ctx, "source", "jobs")
//		}),
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func WithHandlerContextDecorator(fn func(context.Context) context.Context) Option {
	return func(o *runtimeOptions) {
		o.handlerContextDecorator = fn
	}
}

// WithLegacyDirectEnvelope keeps ordinary dispatches on the version-one
// `bus:job` wire route during a workers-first migration. Remove this option only
// after every consumer can process canonical direct deliveries. See the
// [direct delivery migration guide] for backend-specific rollout and rollback.
//
// [direct delivery migration guide]: https://github.com/goforj/queue/blob/main/docs/direct-delivery-migration.md
// @group Queue
func WithLegacyDirectEnvelope() Option {
	return func(o *runtimeOptions) {
		o.legacyDirectEnvelope = true
	}
}

// Queue is the high-level user-facing queue API.
// It composes the queue runtime with the internal orchestration engine.
// @group Queue
type Queue struct {
	q                    queueRuntime
	b                    workflow.Engine
	ctx                  context.Context
	legacyDirectEnvelope bool
}

// newHighLevelQueue constructs the selected physical runtime before attaching the canonical workflow engine.
func newHighLevelQueue(cfg Config, opts ...Option) (*Queue, error) {
	q, err := newRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return newQueueFromRuntime(q, opts...)
}

// newQueueFromRuntime applies root configuration once before registering the single internal workflow engine.
func newQueueFromRuntime(q queueRuntime, opts ...Option) (*Queue, error) {
	var ro runtimeOptions
	ro.apply(opts)
	observer := attachRuntimeObserver(q, ro.observer)
	if ro.workers > 0 && q != nil {
		q = q.Workers(ro.workers)
	}
	if ro.handlerContextDecorator != nil && q != nil {
		q.setHandlerContextDecorator(ro.handlerContextDecorator)
	}
	if observer != nil {
		driver := Driver("")
		if q != nil {
			driver = q.Driver()
		}
		ro.workflowOpts = append(ro.workflowOpts, workflow.WithObserver(workflowObserverAdapter{
			driver:   driver,
			observer: observer,
		}))
	}
	b, err := workflow.New(q, ro.workflowOpts...)
	if err != nil {
		return nil, err
	}
	return &Queue{q: q, b: b, legacyDirectEnvelope: ro.legacyDirectEnvelope}, nil
}

// attachRuntimeObserver composes constructor and option observers before the workflow runtime is built so every layer shares one sink.
func attachRuntimeObserver(q queueRuntime, observer Observer) Observer {
	switch runtime := q.(type) {
	case *nativeQueueRuntime:
		runtime.common.addObserver(observer)
		return runtime.common.observer()
	case *externalQueueRuntime:
		runtime.common.addObserver(observer)
		return runtime.common.observer()
	default:
		return observer
	}
}

type workflowObserverAdapter struct {
	driver   Driver
	observer Observer
}

// Observe converts workflow facts into the canonical event envelope without exposing the internal engine model to applications.
func (a workflowObserverAdapter) Observe(ctx context.Context, event workflow.Event) {
	queueName := event.Queue
	if queueName == "" {
		queueName = "default"
	}
	safeObserve(ctx, a.observer, Event{
		SchemaVersion: event.SchemaVersion,
		EventID:       event.EventID,
		Layer:         eventLayerForKind(EventKind(event.Kind)),
		Kind:          EventKind(event.Kind),
		Driver:        a.driver,
		Queue:         queueName,
		JobType:       event.JobType,
		JobKey:        event.JobKey,
		DispatchID:    event.DispatchID,
		JobID:         event.JobID,
		ChainID:       event.ChainID,
		BatchID:       event.BatchID,
		Attempt:       event.Attempt,
		Duration:      event.Duration,
		Err:           event.Err,
		Time:          event.Time,
	})
}

// NewNull creates a Queue on the null backend.
// @group Constructors
//
// Example: null backend
//
//	q, err := queue.NewNull()
//	if err != nil {
//		return
//	}
//	_ = q
func NewNull(opts ...Option) (*Queue, error) {
	return New(Config{Driver: DriverNull}, opts...)
}

// NewSync creates a Queue on the synchronous in-process backend.
// @group Constructors
//
// Example: sync backend
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	_ = q
func NewSync(opts ...Option) (*Queue, error) {
	return New(Config{Driver: DriverSync}, opts...)
}

// NewWorkerpool creates a Queue on the in-process workerpool backend.
// @group Constructors
//
// Example: workerpool backend
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	_ = q
func NewWorkerpool(opts ...Option) (*Queue, error) {
	return New(Config{Driver: DriverWorkerpool}, opts...)
}

// Register binds a handler for a high-level job type.
// @group Queue
//
// Example: register
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
//		var payload EmailPayload
//		if err := m.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
func (r *Queue) Register(jobType string, handler func(context.Context, Message) error) {
	if r == nil || handler == nil {
		return
	}
	r.b.Register(jobType, func(ctx context.Context, message workflow.Context) error {
		return handler(ctx, messageFromWorkflow(message))
	})
}

// Driver reports the configured backend driver for the underlying queue runtime.
// @group Queue
//
// Example: driver
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	fmt.Println(q.Driver())
//	// Output: sync
func (r *Queue) Driver() Driver {
	if r == nil || r.q == nil {
		return ""
	}
	return r.q.Driver()
}

// WithWorkers sets desired worker concurrency before StartWorkers.
// @group Queue
//
// Example: workers
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	q.WithWorkers(4) // optional; default: runtime.NumCPU() (min 1)
func (r *Queue) WithWorkers(count int) *Queue {
	if r == nil || r.q == nil {
		return r
	}
	r.q = r.q.Workers(count)
	return r
}

// WithContext returns a derived queue handle bound to ctx.
// @group Queue
func (r *Queue) WithContext(ctx context.Context) *Queue {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Dispatch enqueues a high-level job using its application type and exact
// payload bytes together with the queue's bound context.
// @group Queue
//
// Example: dispatch
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
//	if err := q.StartWorkers(context.Background()); err != nil {
//		return
//	}
//	defer q.Shutdown(context.Background())
//	job := queue.NewJob("emails:send").Payload(map[string]any{"id": 1}).OnQueue("default")
//	_, _ = q.Dispatch(job)
func (r *Queue) Dispatch(job Job) (DispatchResult, error) {
	if r == nil {
		return DispatchResult{}, fmt.Errorf("runtime is nil")
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if r.legacyDirectEnvelope {
		legacy, legacyErr := toWorkflowJob(job)
		if legacyErr != nil {
			return DispatchResult{}, legacyErr
		}
		result, dispatchErr := r.b.Dispatch(ctx, legacy)
		return dispatchResultFromWorkflow(result), dispatchErr
	}
	bj, err := toDirectWorkflowJob(job)
	if err != nil {
		return DispatchResult{}, err
	}
	result, err := r.b.DispatchDirect(ctx, bj)
	return dispatchResultFromWorkflow(result), err
}

// toDirectWorkflowJob freezes the canonical root job as exact application
// bytes, avoiding the legacy workflow payload marshaling boundary.
func toDirectWorkflowJob(job Job) (workflow.StoredJob, error) {
	if err := job.validate(); err != nil {
		return workflow.StoredJob{}, err
	}
	var timeout time.Duration
	if job.options.timeout != nil {
		timeout = *job.options.timeout
	}
	var backoff time.Duration
	if job.options.backoff != nil {
		backoff = *job.options.backoff
	}
	return workflow.StoredJob{
		Type:    job.Type,
		Payload: job.PayloadBytes(),
		Options: workflow.JobOptions{
			Queue:     job.options.queueName,
			Delay:     job.options.delay,
			Timeout:   timeout,
			Retry:     optionInt(job.options.maxRetry),
			Backoff:   backoff,
			UniqueFor: job.options.uniqueTTL,
		},
	}, nil
}

// Chain creates a chain builder for sequential workflow execution.
// @group Queue
//
// Example: chain
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	q.Register("first", func(ctx context.Context, m queue.Message) error { return nil })
//	q.Register("second", func(ctx context.Context, m queue.Message) error { return nil })
//	if err := q.StartWorkers(context.Background()); err != nil {
//		return
//	}
//	defer q.Shutdown(context.Background())
//	_, _ = q.Chain(
//		queue.NewJob("first"),
//		queue.NewJob("second"),
//	).OnQueue("default").Dispatch(context.Background())
func (r *Queue) Chain(jobs ...Job) ChainBuilder {
	if r == nil {
		return &chainBuilderAdapter{}
	}
	workflowJobs, err := toWorkflowJobs(jobs)
	if err != nil {
		return &chainBuilderAdapter{err: err}
	}
	return &chainBuilderAdapter{inner: r.b.Chain(workflowJobs...)}
}

// Batch creates a batch builder for fan-out workflow execution.
// @group Queue
//
// Example: batch
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
//	if err := q.StartWorkers(context.Background()); err != nil {
//		return
//	}
//	defer q.Shutdown(context.Background())
//	_, _ = q.Batch(
//		queue.NewJob("emails:send").Payload(map[string]any{"id": 1}),
//		queue.NewJob("emails:send").Payload(map[string]any{"id": 2}),
//	).Name("send-emails").OnQueue("default").Dispatch(context.Background())
func (r *Queue) Batch(jobs ...Job) BatchBuilder {
	if r == nil {
		return &batchBuilderAdapter{}
	}
	workflowJobs, err := toWorkflowJobs(jobs)
	if err != nil {
		return &batchBuilderAdapter{err: err}
	}
	return &batchBuilderAdapter{inner: r.b.Batch(workflowJobs...)}
}

// StartWorkers starts worker processing.
// @group Queue
//
// Example: start workers
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	_ = q.StartWorkers(context.Background())
func (r *Queue) StartWorkers(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.b.StartWorkers(ctx)
}

// Run starts worker processing, blocks until ctx is canceled, then gracefully shuts down.
// @group Queue
//
// Example: run until canceled
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
//	go func() {
//		time.Sleep(100 * time.Millisecond)
//		cancel()
//	}()
//	_ = q.Run(ctx)
func (r *Queue) Run(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.StartWorkers(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	// Use a fresh background context so cancellation triggers graceful shutdown instead of short-circuiting it.
	return r.Shutdown(context.Background())
}

// Shutdown drains workers and closes underlying resources.
// @group Queue
//
// Example: shutdown
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	_ = q.StartWorkers(context.Background())
//	_ = q.Shutdown(context.Background())
func (r *Queue) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.b.Shutdown(ctx)
}

// FindChain returns current chain state by ID.
// @group Queue
//
// Example: find chain
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	q.Register("first", func(ctx context.Context, m queue.Message) error { return nil })
//	chainID, err := q.Chain(queue.NewJob("first")).Dispatch(context.Background())
//	if err != nil {
//		return
//	}
//	_, _ = q.FindChain(context.Background(), chainID)
func (r *Queue) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	if r == nil {
		return ChainState{}, fmt.Errorf("runtime is nil")
	}
	state, err := r.b.FindChain(ctx, chainID)
	return chainStateFromWorkflow(state), err
}

// FindBatch returns current batch state by ID.
// @group Queue
//
// Example: find batch
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
//	batchID, err := q.Batch(queue.NewJob("emails:send")).Dispatch(context.Background())
//	if err != nil {
//		return
//	}
//	_, _ = q.FindBatch(context.Background(), batchID)
func (r *Queue) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	if r == nil {
		return BatchState{}, fmt.Errorf("runtime is nil")
	}
	state, err := r.b.FindBatch(ctx, batchID)
	return batchStateFromWorkflow(state), err
}

// Prune deletes old workflow state records.
// @group Queue
//
// Example: prune workflow state
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	_ = q.Prune(context.Background(), time.Now().Add(-24*time.Hour))
func (r *Queue) Prune(ctx context.Context, before time.Time) error {
	if r == nil {
		return fmt.Errorf("runtime is nil")
	}
	return r.b.Prune(ctx, before)
}

// Pause pauses consumption for a queue when supported by the underlying driver.
// See the README "Queue Backends" table for Pause/Resume support and
// docs/backend-guarantees.md (Capability Matrix) for broader backend differences.
// @group Queue
//
// Example: pause queue
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	if queue.SupportsPause(q) {
//		_ = q.Pause(context.Background(), "default")
//	}
func (r *Queue) Pause(ctx context.Context, queueName string) error {
	if r == nil || r.q == nil {
		return fmt.Errorf("runtime is nil")
	}
	controller, ok := r.q.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

// Resume resumes consumption for a queue when supported by the underlying driver.
// @group Queue
//
// Example: resume queue
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	if queue.SupportsPause(q) {
//		_ = q.Resume(context.Background(), "default")
//	}
func (r *Queue) Resume(ctx context.Context, queueName string) error {
	if r == nil || r.q == nil {
		return fmt.Errorf("runtime is nil")
	}
	controller, ok := r.q.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

// Stats returns a normalized snapshot when supported by the underlying driver.
// @group Queue
//
// Example: stats
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	if queue.SupportsNativeStats(q) {
//		_, _ = q.Stats(context.Background())
//	}
func (r *Queue) Stats(ctx context.Context) (StatsSnapshot, error) {
	if r == nil || r.q == nil {
		return StatsSnapshot{}, fmt.Errorf("runtime is nil")
	}
	provider, ok := r.q.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", r.q.Driver())
	}
	return provider.Stats(ctx)
}

// Ready validates queue backend readiness for dispatch/worker operation.
// @group Queue
//
// Example: queue ready
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	fmt.Println(q.Ready(context.Background()) == nil)
//	// true
func (r *Queue) Ready(ctx context.Context) error {
	if r == nil || r.q == nil {
		return fmt.Errorf("runtime is nil")
	}
	checker, ok := r.q.(interface{ Ready(context.Context) error })
	if !ok {
		return nil
	}
	return checker.Ready(ctx)
}

// ChainBuilder is the high-level chain workflow builder.
// @group Queue
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

type chainBuilderAdapter struct {
	inner           workflow.ChainBuilder
	err             error
	dispatchGuard   func() func()
	dispatchContext func(context.Context) context.Context
	onAccepted      func(string)
	onRejected      func(string)
}

// OnQueue forwards fluent queue selection while preserving any earlier conversion failure.
func (b *chainBuilderAdapter) OnQueue(queue string) ChainBuilder {
	if b.inner != nil {
		b.inner = b.inner.OnQueue(queue)
	}
	return b
}

// Catch forwards the explicitly ephemeral chain failure callback.
func (b *chainBuilderAdapter) Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder {
	if b.inner != nil {
		b.inner = b.inner.Catch(chainCatchToWorkflow(fn))
	}
	return b
}

// Finally forwards the explicitly ephemeral chain terminal callback.
func (b *chainBuilderAdapter) Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder {
	if b.inner != nil {
		b.inner = b.inner.Finally(chainFinallyToWorkflow(fn))
	}
	return b
}

// Dispatch returns deferred builder errors before asking the internal engine to create state.
func (b *chainBuilderAdapter) Dispatch(ctx context.Context) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	if b.inner == nil {
		return "", fmt.Errorf("chain builder is nil")
	}
	if b.dispatchGuard != nil {
		release := b.dispatchGuard()
		defer release()
	}
	if b.dispatchContext != nil {
		ctx = b.dispatchContext(ctx)
	}
	chainID, err := b.inner.Dispatch(ctx)
	if err == nil && b.onAccepted != nil {
		b.onAccepted(chainID)
	} else if err != nil && chainID != "" && b.onRejected != nil {
		b.onRejected(chainID)
	}
	return chainID, err
}

// BatchBuilder is the high-level batch workflow builder.
// @group Queue
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

type batchBuilderAdapter struct {
	inner           workflow.BatchBuilder
	err             error
	dispatchGuard   func() func()
	dispatchContext func(context.Context) context.Context
	onAccepted      func(string)
	onRejected      func(string)
}

// Name forwards the application-facing batch label while preserving any earlier conversion failure.
func (b *batchBuilderAdapter) Name(name string) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Name(name)
	}
	return b
}

// OnQueue forwards fluent queue selection while preserving any earlier conversion failure.
func (b *batchBuilderAdapter) OnQueue(queue string) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.OnQueue(queue)
	}
	return b
}

// AllowFailures forwards the aggregate failure policy to the internal engine.
func (b *batchBuilderAdapter) AllowFailures() BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.AllowFailures()
	}
	return b
}

// Progress forwards the explicitly ephemeral batch progress callback.
func (b *batchBuilderAdapter) Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Progress(batchStateCallbackToWorkflow(fn))
	}
	return b
}

// Then forwards the explicitly ephemeral batch success callback.
func (b *batchBuilderAdapter) Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Then(batchStateCallbackToWorkflow(fn))
	}
	return b
}

// Catch forwards the explicitly ephemeral batch failure callback.
func (b *batchBuilderAdapter) Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Catch(batchCatchToWorkflow(fn))
	}
	return b
}

// Finally forwards the explicitly ephemeral batch terminal callback.
func (b *batchBuilderAdapter) Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Finally(batchStateCallbackToWorkflow(fn))
	}
	return b
}

// Dispatch returns deferred builder errors before asking the internal engine to create state.
func (b *batchBuilderAdapter) Dispatch(ctx context.Context) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	if b.inner == nil {
		return "", fmt.Errorf("batch builder is nil")
	}
	if b.dispatchGuard != nil {
		release := b.dispatchGuard()
		defer release()
	}
	if b.dispatchContext != nil {
		ctx = b.dispatchContext(ctx)
	}
	batchID, err := b.inner.Dispatch(ctx)
	if err == nil && b.onAccepted != nil {
		b.onAccepted(batchID)
	} else if err != nil && batchID != "" && b.onRejected != nil {
		b.onRejected(batchID)
	}
	return batchID, err
}

// toWorkflowJobs converts a canonical job slice once so production and fake
// workflow builders share validation and payload ownership rules.
func toWorkflowJobs(jobs []Job) ([]workflow.Job, error) {
	converted := make([]workflow.Job, 0, len(jobs))
	for _, job := range jobs {
		workflowJob, err := toWorkflowJob(job)
		if err != nil {
			return nil, err
		}
		converted = append(converted, workflowJob)
	}
	return converted, nil
}

// toWorkflowJob converts the canonical root job into the engine's private compatibility model without changing payload bytes.
func toWorkflowJob(job Job) (workflow.Job, error) {
	if err := job.validate(); err != nil {
		return workflow.Job{}, err
	}
	if job.Type == "" {
		return workflow.Job{}, fmt.Errorf("job type is required")
	}
	payload := job.PayloadBytes()
	var busPayload any
	if payload != nil {
		busPayload = json.RawMessage(payload)
	}
	j := workflow.NewJob(job.Type, busPayload)
	if job.options.queueName != "" {
		j = j.OnQueue(job.options.queueName)
	}
	if job.options.delay > 0 {
		j = j.Delay(job.options.delay)
	}
	if job.options.timeout != nil {
		j = j.Timeout(*job.options.timeout)
	}
	if job.options.maxRetry != nil {
		j = j.Retry(*job.options.maxRetry)
	}
	if job.options.backoff != nil {
		j = j.Backoff(*job.options.backoff)
	}
	if job.options.uniqueTTL > 0 {
		j = j.UniqueFor(job.options.uniqueTTL)
	}
	return j, nil
}
