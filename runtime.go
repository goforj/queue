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

// RuntimeOption configures the high-level workflow runtime.
// @group Queue
type RuntimeOption func(*runtimeOptions)

type runtimeOptions struct {
	busOpts []bus.Option
}

func (o *runtimeOptions) apply(opts []RuntimeOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
}

// WithWorkflowObserver installs a workflow lifecycle observer.
// @group Queue
func WithWorkflowObserver(observer WorkflowObserver) RuntimeOption {
	return func(o *runtimeOptions) {
		o.busOpts = append(o.busOpts, bus.WithObserver(observer))
	}
}

// WithWorkflowStore overrides the workflow orchestration store.
// @group Queue
func WithWorkflowStore(store WorkflowStore) RuntimeOption {
	return func(o *runtimeOptions) {
		o.busOpts = append(o.busOpts, bus.WithStore(store))
	}
}

// WithWorkflowClock overrides the workflow runtime clock.
// @group Queue
func WithWorkflowClock(clock func() time.Time) RuntimeOption {
	return func(o *runtimeOptions) {
		o.busOpts = append(o.busOpts, bus.WithClock(clock))
	}
}

// WithMiddleware appends queue workflow middleware.
// @group Queue
func WithMiddleware(middlewares ...Middleware) RuntimeOption {
	return func(o *runtimeOptions) {
		o.busOpts = append(o.busOpts, bus.WithMiddleware(middlewares...))
	}
}

// Queue is the high-level user-facing queue API.
// It composes the queue runtime with the internal orchestration engine.
// @group Queue
type Queue struct {
	q QueueRuntime
	b bus.Bus
}

func newHighLevelQueue(cfg Config, opts ...RuntimeOption) (*Queue, error) {
	q, err := NewQueue(cfg)
	if err != nil {
		return nil, err
	}
	return newQueueFromRuntime(q, opts...)
}

func newQueueFromRuntime(q QueueRuntime, opts ...RuntimeOption) (*Queue, error) {
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
func NewNull() (*Queue, error) {
	return New(Config{Driver: DriverNull})
}

// NewSync creates a Queue on the synchronous in-process backend.
// @group Constructors
func NewSync() (*Queue, error) {
	return New(Config{Driver: DriverSync})
}

// NewWorkerpool creates a Queue on the in-process workerpool backend.
// @group Constructors
func NewWorkerpool() (*Queue, error) {
	return New(Config{Driver: DriverWorkerpool})
}

// NewDatabase creates a Queue on the SQL backend.
// @group Constructors
func NewDatabase(driverName, dsn string) (*Queue, error) {
	return New(Config{
		Driver:         DriverDatabase,
		DatabaseDriver: driverName,
		DatabaseDSN:    dsn,
	})
}

// NewRedis creates a Queue on the Redis backend.
// @group Constructors
func NewRedis(addr string) (*Queue, error) {
	return New(Config{
		Driver:    DriverRedis,
		RedisAddr: addr,
	})
}

// NewNATS creates a Queue on the NATS backend.
// @group Constructors
func NewNATS(url string) (*Queue, error) {
	return New(Config{
		Driver:  DriverNATS,
		NATSURL: url,
	})
}

// NewSQS creates a Queue on the SQS backend.
// @group Constructors
func NewSQS(region string) (*Queue, error) {
	return New(Config{
		Driver:    DriverSQS,
		SQSRegion: region,
	})
}

// NewRabbitMQ creates a Queue on the RabbitMQ backend.
// @group Constructors
func NewRabbitMQ(url string) (*Queue, error) {
	return New(Config{
		Driver:      DriverRabbitMQ,
		RabbitMQURL: url,
	})
}

// Register binds a handler for a high-level job type.
// @group Queue
func (r *Queue) Register(jobType string, handler func(context.Context, Context) error) {
	if r == nil {
		return
	}
	r.b.Register(jobType, handler)
}

// Driver reports the configured backend driver for the underlying queue runtime.
// @group Queue
func (r *Queue) Driver() Driver {
	if r == nil || r.q == nil {
		return ""
	}
	return r.q.Driver()
}

// Workers sets desired worker concurrency before StartWorkers.
// @group Queue
func (r *Queue) Workers(count int) *Queue {
	if r == nil || r.q == nil {
		return r
	}
	r.q = r.q.Workers(count)
	return r
}

// Dispatch enqueues a high-level job.
// @group Queue
func (r *Queue) Dispatch(ctx context.Context, job Job) (DispatchResult, error) {
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
func (r *Queue) StartWorkers(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.b.StartWorkers(ctx)
}

// Shutdown drains workers and closes underlying resources.
// @group Queue
func (r *Queue) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.b.Shutdown(ctx)
}

// FindChain returns current chain state by ID.
// @group Queue
func (r *Queue) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	if r == nil {
		return ChainState{}, fmt.Errorf("runtime is nil")
	}
	return r.b.FindChain(ctx, chainID)
}

// FindBatch returns current batch state by ID.
// @group Queue
func (r *Queue) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	if r == nil {
		return BatchState{}, fmt.Errorf("runtime is nil")
	}
	return r.b.FindBatch(ctx, batchID)
}

// Prune deletes old workflow state records.
// @group Queue
func (r *Queue) Prune(ctx context.Context, before time.Time) error {
	if r == nil {
		return fmt.Errorf("runtime is nil")
	}
	return r.b.Prune(ctx, before)
}

// Pause pauses consumption for a queue when supported by the underlying driver.
// @group Queue
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

// UnderlyingQueue returns the low-level queue runtime used by this high-level runtime.
// @group Queue
func (r *Queue) UnderlyingQueue() QueueRuntime {
	if r == nil {
		return nil
	}
	return r.q
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
