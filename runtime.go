package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/goforj/queue/bus"
)

// Context is the handler context passed to the high-level queue runtime.
// It exposes workflow/job metadata and payload binding helpers.
// @group Queue
type Context = bus.Context

// DispatchResult describes a high-level dispatch operation.
// @group Queue
type DispatchResult = bus.DispatchResult

// ChainState is the persisted view of a chain workflow.
// @group Queue
type ChainState = bus.ChainState

// BatchState is the persisted view of a batch workflow.
// @group Queue
type BatchState = bus.BatchState

// WorkflowEventKind identifies high-level workflow runtime lifecycle events.
// @group Queue
type WorkflowEventKind = bus.EventKind

// WorkflowEvent is emitted by the high-level workflow runtime observer hooks.
// @group Queue
type WorkflowEvent = bus.Event

// WorkflowObserver receives high-level workflow runtime events.
// @group Queue
type WorkflowObserver = bus.Observer

// WorkflowObserverFunc adapts a function to a workflow observer.
// @group Queue
type WorkflowObserverFunc = bus.ObserverFunc

// Next invokes the next middleware/handler in the queue middleware chain.
// @group Queue
type Next = bus.Next

// Middleware applies behavior around high-level workflow/job execution.
// @group Queue
type Middleware = bus.Middleware

// MiddlewareFunc adapts a function to queue middleware.
// @group Queue
type MiddlewareFunc = bus.MiddlewareFunc

// RetryPolicy is a pass-through middleware policy helper.
// @group Queue
type RetryPolicy = bus.RetryPolicy

// SkipWhen skips execution when the predicate matches.
// @group Queue
type SkipWhen = bus.SkipWhen

// FailOnError converts matched errors into fatal (non-retryable) failures.
// @group Queue
type FailOnError = bus.FailOnError

// RateLimiter is used by RateLimit middleware.
// @group Queue
type RateLimiter = bus.RateLimiter

// RateLimit applies rate limiting before job execution.
// @group Queue
type RateLimit = bus.RateLimit

// Lock is used by overlap prevention middleware.
// @group Queue
type Lock = bus.Lock

// Locker acquires locks for overlap prevention middleware.
// @group Queue
type Locker = bus.Locker

// WithoutOverlapping prevents concurrent execution for the same key.
// @group Queue
type WithoutOverlapping = bus.WithoutOverlapping

// WorkflowStore is the orchestration state store used for chains/batches/callbacks.
// @group Queue
type WorkflowStore = bus.Store

// ErrWorkflowNotFound indicates a workflow state record is not present.
// @group Queue
var ErrWorkflowNotFound = bus.ErrNotFound

// Option configures the high-level workflow runtime.
// @group Queue
type Option func(*runtimeOptions)

type runtimeOptions struct {
	busOpts []bus.Option
}

func (o *runtimeOptions) apply(opts []Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
}

// WithObserver installs a workflow lifecycle observer.
// @group Queue
//
// Example: workflow observer
//
//	observer := queue.WorkflowObserverFunc(func(event queue.WorkflowEvent) {
//		_ = event.Kind
//	})
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithObserver(observer))
//	if err != nil {
//		return
//	}
//	_ = q
func WithObserver(observer WorkflowObserver) Option {
	return func(o *runtimeOptions) {
		o.busOpts = append(o.busOpts, bus.WithObserver(observer))
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
		o.busOpts = append(o.busOpts, bus.WithStore(store))
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
		o.busOpts = append(o.busOpts, bus.WithClock(clock))
	}
}

// WithMiddleware appends queue workflow middleware.
// @group Queue
//
// Example: middleware
//
//	mw := queue.MiddlewareFunc(func(ctx context.Context, j queue.Context, next queue.Next) error {
//		return next(ctx, j)
//	})
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithMiddleware(mw))
//	if err != nil {
//		return
//	}
//	_ = q
func WithMiddleware(middlewares ...Middleware) Option {
	return func(o *runtimeOptions) {
		o.busOpts = append(o.busOpts, bus.WithMiddleware(middlewares...))
	}
}

// Queue is the high-level user-facing queue API.
// It composes the queue runtime with the internal orchestration engine.
// @group Queue
type Queue struct {
	q queueRuntime
	b bus.Bus
}

func newHighLevelQueue(cfg Config, opts ...Option) (*Queue, error) {
	q, err := newRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return newQueueFromRuntime(q, opts...)
}

func newQueueFromRuntime(q queueRuntime, opts ...Option) (*Queue, error) {
	var ro runtimeOptions
	ro.apply(opts)
	b, err := bus.New(q, ro.busOpts...)
	if err != nil {
		return nil, err
	}
	return &Queue{q: q, b: b}, nil
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
func NewNull() (*Queue, error) {
	return New(Config{Driver: DriverNull})
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
func NewSync() (*Queue, error) {
	return New(Config{Driver: DriverSync})
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
func NewWorkerpool() (*Queue, error) {
	return New(Config{Driver: DriverWorkerpool})
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
//	q.Register("emails:send", func(ctx context.Context, j queue.Context) error {
//		var payload EmailPayload
//		if err := j.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
func (r *Queue) Register(jobType string, handler func(context.Context, Context) error) {
	if r == nil {
		return
	}
	r.b.Register(jobType, handler)
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

// Workers sets desired worker concurrency before StartWorkers.
// @group Queue
//
// Example: workers
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	q.Workers(4)
func (r *Queue) Workers(count int) *Queue {
	if r == nil || r.q == nil {
		return r
	}
	r.q = r.q.Workers(count)
	return r
}

// Dispatch enqueues a high-level job using context.Background.
// @group Queue
//
// Example: dispatch
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	q.Register("emails:send", func(ctx context.Context, j queue.Context) error { return nil })
//	job := queue.NewJob("emails:send").Payload(map[string]any{"id": 1}).OnQueue("default")
//	_, _ = q.Dispatch(job)
func (r *Queue) Dispatch(job Job) (DispatchResult, error) {
	return r.DispatchCtx(context.Background(), job)
}

// DispatchCtx enqueues a high-level job using the provided context.
// @group Queue
func (r *Queue) DispatchCtx(ctx context.Context, job Job) (DispatchResult, error) {
	if r == nil {
		return DispatchResult{}, fmt.Errorf("runtime is nil")
	}
	bj, err := toBusJob(job)
	if err != nil {
		return DispatchResult{}, err
	}
	return r.b.Dispatch(ctx, bj)
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
//	q.Register("first", func(ctx context.Context, j queue.Context) error { return nil })
//	q.Register("second", func(ctx context.Context, j queue.Context) error { return nil })
//	_, _ = q.Chain(
//		queue.NewJob("first"),
//		queue.NewJob("second"),
//	).OnQueue("default").Dispatch(context.Background())
func (r *Queue) Chain(jobs ...Job) ChainBuilder {
	if r == nil {
		return &chainBuilderAdapter{}
	}
	busJobs := make([]bus.Job, 0, len(jobs))
	for _, job := range jobs {
		bj, err := toBusJob(job)
		if err != nil {
			return &chainBuilderAdapter{err: err}
		}
		busJobs = append(busJobs, bj)
	}
	return &chainBuilderAdapter{inner: r.b.Chain(busJobs...)}
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
//	q.Register("emails:send", func(ctx context.Context, j queue.Context) error { return nil })
//	_, _ = q.Batch(
//		queue.NewJob("emails:send").Payload(map[string]any{"id": 1}),
//		queue.NewJob("emails:send").Payload(map[string]any{"id": 2}),
//	).Name("send-emails").OnQueue("default").Dispatch(context.Background())
func (r *Queue) Batch(jobs ...Job) BatchBuilder {
	if r == nil {
		return &batchBuilderAdapter{}
	}
	busJobs := make([]bus.Job, 0, len(jobs))
	for _, job := range jobs {
		bj, err := toBusJob(job)
		if err != nil {
			return &batchBuilderAdapter{err: err}
		}
		busJobs = append(busJobs, bj)
	}
	return &batchBuilderAdapter{inner: r.b.Batch(busJobs...)}
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
//	q.Register("first", func(ctx context.Context, j queue.Context) error { return nil })
//	chainID, err := q.Chain(queue.NewJob("first")).Dispatch(context.Background())
//	if err != nil {
//		return
//	}
//	_, _ = q.FindChain(context.Background(), chainID)
func (r *Queue) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	if r == nil {
		return ChainState{}, fmt.Errorf("runtime is nil")
	}
	return r.b.FindChain(ctx, chainID)
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
//	q.Register("emails:send", func(ctx context.Context, j queue.Context) error { return nil })
//	batchID, err := q.Batch(queue.NewJob("emails:send")).Dispatch(context.Background())
//	if err != nil {
//		return
//	}
//	_, _ = q.FindBatch(context.Background(), batchID)
func (r *Queue) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	if r == nil {
		return BatchState{}, fmt.Errorf("runtime is nil")
	}
	return r.b.FindBatch(ctx, batchID)
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

// ChainBuilder is the high-level chain workflow builder.
// @group Queue
type ChainBuilder interface {
	OnQueue(queue string) ChainBuilder
	Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder
	Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder
	Dispatch(ctx context.Context) (string, error)
}

type chainBuilderAdapter struct {
	inner bus.ChainBuilder
	err   error
}

func (b *chainBuilderAdapter) OnQueue(queue string) ChainBuilder {
	if b.inner != nil {
		b.inner = b.inner.OnQueue(queue)
	}
	return b
}

func (b *chainBuilderAdapter) Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder {
	if b.inner != nil {
		b.inner = b.inner.Catch(fn)
	}
	return b
}

func (b *chainBuilderAdapter) Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder {
	if b.inner != nil {
		b.inner = b.inner.Finally(fn)
	}
	return b
}

func (b *chainBuilderAdapter) Dispatch(ctx context.Context) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	if b.inner == nil {
		return "", fmt.Errorf("chain builder is nil")
	}
	return b.inner.Dispatch(ctx)
}

// BatchBuilder is the high-level batch workflow builder.
// @group Queue
type BatchBuilder interface {
	Name(name string) BatchBuilder
	OnQueue(queue string) BatchBuilder
	AllowFailures() BatchBuilder
	Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder
	Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	Dispatch(ctx context.Context) (string, error)
}

type batchBuilderAdapter struct {
	inner bus.BatchBuilder
	err   error
}

func (b *batchBuilderAdapter) Name(name string) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Name(name)
	}
	return b
}

func (b *batchBuilderAdapter) OnQueue(queue string) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.OnQueue(queue)
	}
	return b
}

func (b *batchBuilderAdapter) AllowFailures() BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.AllowFailures()
	}
	return b
}

func (b *batchBuilderAdapter) Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Progress(fn)
	}
	return b
}

func (b *batchBuilderAdapter) Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Then(fn)
	}
	return b
}

func (b *batchBuilderAdapter) Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Catch(fn)
	}
	return b
}

func (b *batchBuilderAdapter) Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	if b.inner != nil {
		b.inner = b.inner.Finally(fn)
	}
	return b
}

func (b *batchBuilderAdapter) Dispatch(ctx context.Context) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	if b.inner == nil {
		return "", fmt.Errorf("batch builder is nil")
	}
	return b.inner.Dispatch(ctx)
}

func toBusJob(job Job) (bus.Job, error) {
	if err := job.validate(); err != nil {
		return bus.Job{}, err
	}
	if job.Type == "" {
		return bus.Job{}, fmt.Errorf("job type is required")
	}
	payload := job.PayloadBytes()
	var busPayload any
	if payload != nil {
		busPayload = json.RawMessage(payload)
	}
	j := bus.NewJob(job.Type, busPayload)
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
