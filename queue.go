package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/goforj/queue/busruntime"
)

type queueRuntime interface {
	// Driver returns the active queue driver.
	// @group Driver Integration
	Driver() Driver

	// WithContext returns a derived queue runtime handle bound to ctx.
	// @group Driver Integration
	WithContext(ctx context.Context) queueRuntime

	// Dispatch submits a typed job payload using the default queue.
	// @group Driver Integration
	Dispatch(job any) error

	// Register associates a handler with a job type.
	// @group Driver Integration
	Register(jobType string, handler Handler)

	// StartWorkers starts worker execution.
	// @group Driver Integration
	StartWorkers(ctx context.Context) error

	// Workers sets desired worker concurrency before StartWorkers.
	// @group Driver Integration
	Workers(count int) queueRuntime

	// Shutdown drains running work and releases resources.
	// @group Driver Integration
	Shutdown(ctx context.Context) error

	// Ready checks backend readiness for dispatch/worker operation.
	// @group Driver Integration
	Ready(ctx context.Context) error
}

// WorkerpoolConfig configures the in-memory workerpool q.
// @group Config
type WorkerpoolConfig struct {
	Workers           int
	QueueCapacity     int
	DefaultJobTimeout time.Duration
}

func (c WorkerpoolConfig) normalize() WorkerpoolConfig {
	c.Workers = defaultWorkerCount(c.Workers)
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = c.Workers
	}
	return c
}

// Config configures queue creation for New (and advanced driver/runtime interop).
// @group Config
type Config struct {
	Driver   Driver
	Observer Observer
	Logger   Logger

	DefaultQueue string
}

type queueBackend interface {
	Driver() Driver
	Dispatch(ctx context.Context, job Job) error
	Shutdown(ctx context.Context) error
}

type runtimeQueueBackend interface {
	queueBackend
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
}

func newSyncQueue() queueBackend {
	return newLocalQueueWithConfig(DriverSync, WorkerpoolConfig{})
}

// New creates the high-level Queue API based on Config.Driver.
// @group Constructors
//
// Example: create a queue and dispatch a workflow-capable job
//
//	q, err := queue.New(queue.Config{Driver: queue.DriverWorkerpool})
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
//	_ = q.WithWorkers(1).StartWorkers(context.Background()) // optional; default: runtime.NumCPU() (min 1)
//	defer q.Shutdown(context.Background())
//	_, _ = q.Dispatch(
//		queue.NewJob("emails:send").
//			Payload(EmailPayload{ID: 1}).
//			OnQueue("default"),
//	)
func New(cfg Config, opts ...Option) (*Queue, error) {
	return newHighLevelQueue(cfg, opts...)
}

func newRuntime(cfg Config) (queueRuntime, error) {
	cfg = cfg.normalize()

	var q queueBackend
	var err error
	switch cfg.Driver {
	case DriverNull:
		q = newNullQueue()
	case DriverSync:
		q = newSyncQueue()
	case DriverWorkerpool:
		q = newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{})
	case DriverDatabase:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverRedis:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverNATS:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverSQS:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverRabbitMQ:
		return nil, optionalDriverMovedError(cfg.Driver)
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
	if err != nil {
		return nil, err
	}
	var runtime runtimeQueueBackend
	if native, ok := q.(runtimeQueueBackend); ok {
		runtime = native
	}
	common := &queueCommon{
		inner:  newObservedQueue(q, cfg.Driver, cfg.Observer),
		cfg:    cfg,
		driver: cfg.Driver,
	}
	if runtime != nil {
		return &nativeQueueRuntime{
			common:     common,
			runtime:    runtime,
			registered: make(map[string]Handler),
		}, nil
	}
	return &externalQueueRuntime{
		common:     common,
		registered: make(map[string]Handler),
	}, nil
}

func (cfg Config) normalize() Config {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = "default"
	}
	return cfg
}

type queueCommon struct {
	inner  queueBackend
	cfg    Config
	driver Driver
	ctx    context.Context
}

type nativeQueueRuntime struct {
	common  *queueCommon
	runtime runtimeQueueBackend

	mu         sync.Mutex
	registered map[string]Handler
	started    bool
	workers    int
}

type externalQueueRuntime struct {
	common *queueCommon

	mu         sync.Mutex
	registered map[string]Handler
	worker     runtimeWorkerBackend
	started    bool
	workers    int
	newWorker  driverWorkerFactory
}

type runtimeWorkerBackend interface {
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

func (q *queueCommon) Driver() Driver {
	return q.driver
}

func (q *queueCommon) context() context.Context {
	if q == nil || q.ctx == nil {
		return context.Background()
	}
	return q.ctx
}

func (q *queueCommon) WithContext(ctx context.Context) *queueCommon {
	if q == nil {
		return nil
	}
	clone := *q
	clone.ctx = ctx
	return &clone
}

func (q *queueCommon) Dispatch(job any) error {
	dispatchJob, err := q.jobFromAny(job)
	if err != nil {
		return err
	}
	return q.inner.Dispatch(q.context(), dispatchJob)
}

func (q *nativeQueueRuntime) Driver() Driver         { return q.common.Driver() }
func (q *nativeQueueRuntime) Dispatch(job any) error { return q.common.Dispatch(job) }
func (q *nativeQueueRuntime) WithContext(ctx context.Context) queueRuntime {
	if q == nil {
		return nil
	}
	clone := *q
	clone.common = q.common.WithContext(ctx)
	return &clone
}

func (q *externalQueueRuntime) Driver() Driver         { return q.common.Driver() }
func (q *externalQueueRuntime) Dispatch(job any) error { return q.common.Dispatch(job) }
func (q *externalQueueRuntime) WithContext(ctx context.Context) queueRuntime {
	if q == nil {
		return nil
	}
	clone := *q
	clone.common = q.common.WithContext(ctx)
	return &clone
}

func (q *nativeQueueRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if handler == nil {
		q.Register(jobType, nil)
		return
	}
	q.Register(jobType, func(ctx context.Context, job Job) error {
		return handler(ctx, job)
	})
}

func (q *externalQueueRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if handler == nil {
		q.Register(jobType, nil)
		return
	}
	q.Register(jobType, func(ctx context.Context, job Job) error {
		return handler(ctx, job)
	})
}

func (q *nativeQueueRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	return q.common.dispatchBusJob(ctx, jobType, payload, opts)
}

func (q *externalQueueRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	return q.common.dispatchBusJob(ctx, jobType, payload, opts)
}

func (q *nativeQueueRuntime) Register(jobType string, handler Handler) {
	q.mu.Lock()
	if q.registered == nil {
		q.registered = make(map[string]Handler)
	}
	q.registered[jobType] = handler
	started := q.started
	q.mu.Unlock()

	if started {
		q.runtime.Register(jobType, q.common.wrapRegisteredHandler(jobType, handler))
	}
}

func (q *externalQueueRuntime) Register(jobType string, handler Handler) {
	q.mu.Lock()
	if q.registered == nil {
		q.registered = make(map[string]Handler)
	}
	q.registered[jobType] = handler
	w := q.worker
	started := q.started
	q.mu.Unlock()

	if started && w != nil {
		w.Register(jobType, q.common.wrapRegisteredHandler(jobType, handler))
	}
}

func (q *nativeQueueRuntime) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return nil
	}
	registered := make(map[string]Handler, len(q.registered))
	for jobType, handler := range q.registered {
		registered[jobType] = handler
	}
	q.mu.Unlock()

	for jobType, handler := range registered {
		q.runtime.Register(jobType, q.common.wrapRegisteredHandler(jobType, handler))
	}
	if err := q.runtime.StartWorkers(ctx); err != nil {
		return err
	}
	q.mu.Lock()
	q.started = true
	q.mu.Unlock()
	return nil
}

func (q *externalQueueRuntime) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return nil
	}
	workers := q.workers
	registered := make(map[string]Handler, len(q.registered))
	for jobType, handler := range q.registered {
		registered[jobType] = handler
	}
	q.mu.Unlock()

	var (
		w   runtimeWorkerBackend
		err error
	)
	if q.newWorker != nil {
		driverWorker, e := q.newWorker(defaultWorkerCount(workers))
		if e != nil {
			return e
		}
		w = driverWorkerBackendAdapter{driverWorker}
	} else {
		w, err = newExternalWorker(q.common.cfg, workers)
		if err != nil {
			return err
		}
	}
	for jobType, handler := range registered {
		w.Register(jobType, q.common.wrapRegisteredHandler(jobType, handler))
	}
	if err := w.StartWorkers(ctx); err != nil {
		return err
	}
	q.mu.Lock()
	q.worker = w
	q.started = true
	q.mu.Unlock()
	return nil
}

func (q *nativeQueueRuntime) Workers(count int) queueRuntime {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && count > 0 {
		q.workers = count
	}
	return q
}

func (q *externalQueueRuntime) Workers(count int) queueRuntime {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && count > 0 {
		q.workers = count
	}
	return q
}

func (q *nativeQueueRuntime) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	wasStarted := q.started
	q.started = false
	q.mu.Unlock()

	if wasStarted {
		return q.runtime.Shutdown(ctx)
	}
	return nil
}

func (q *externalQueueRuntime) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	w := q.worker
	wasStarted := q.started
	q.started = false
	q.worker = nil
	q.mu.Unlock()

	if wasStarted {
		if w != nil {
			if err := w.Shutdown(ctx); err != nil {
				return err
			}
		}
	}
	return q.common.inner.Shutdown(ctx)
}

func (q *queueCommon) Pause(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

func (q *queueCommon) Resume(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

func (q *queueCommon) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := q.inner.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", q.Driver())
	}
	return provider.Stats(ctx)
}

func (q *queueCommon) Ready(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return runtimeReadyCheck(ctx, q.inner)
}

func (q *nativeQueueRuntime) Pause(ctx context.Context, queueName string) error {
	return q.common.Pause(ctx, queueName)
}
func (q *nativeQueueRuntime) Resume(ctx context.Context, queueName string) error {
	return q.common.Resume(ctx, queueName)
}
func (q *nativeQueueRuntime) Stats(ctx context.Context) (StatsSnapshot, error) {
	return q.common.Stats(ctx)
}
func (q *nativeQueueRuntime) Ready(ctx context.Context) error {
	return q.common.Ready(ctx)
}
func (q *externalQueueRuntime) Pause(ctx context.Context, queueName string) error {
	return q.common.Pause(ctx, queueName)
}
func (q *externalQueueRuntime) Resume(ctx context.Context, queueName string) error {
	return q.common.Resume(ctx, queueName)
}
func (q *externalQueueRuntime) Stats(ctx context.Context) (StatsSnapshot, error) {
	return q.common.Stats(ctx)
}
func (q *externalQueueRuntime) Ready(ctx context.Context) error {
	return q.common.Ready(ctx)
}

func (q *queueCommon) wrapRegisteredHandler(jobType string, handler Handler) Handler {
	if handler == nil || q.cfg.Observer == nil {
		return handler
	}
	// Redis worker emits process lifecycle events natively.
	// Skip shared handler wrapping to avoid duplicate process_* events.
	if q.cfg.Driver == DriverRedis {
		return handler
	}
	return wrapObservedHandler(q.cfg.Observer, q.cfg.Driver, "", jobType, handler)
}

func (q *queueCommon) dispatchBusJob(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	job := NewJob(jobType).Payload(payload)
	if opts.Queue != "" {
		job = job.OnQueue(opts.Queue)
	}
	if opts.Delay > 0 {
		job = job.Delay(opts.Delay)
	}
	if opts.Timeout > 0 {
		job = job.Timeout(opts.Timeout)
	}
	if opts.Retry > 0 {
		job = job.Retry(opts.Retry)
	}
	if opts.Backoff > 0 {
		job = job.Backoff(opts.Backoff)
	}
	if opts.UniqueFor > 0 {
		job = job.UniqueFor(opts.UniqueFor)
	}
	return q.inner.Dispatch(ctx, job)
}

func newExternalWorker(cfg Config, concurrency int) (runtimeWorkerBackend, error) {
	switch cfg.Driver {
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}

type driverQueueBackendAdapter struct {
	driverQueueBackend
}

type driverRuntimeQueueBackendAdapter struct {
	driverRuntimeQueueBackend
}

type driverWorkerBackendAdapter struct {
	driverWorkerBackend
}

func (a driverQueueBackendAdapter) Pause(ctx context.Context, queueName string) error {
	controller, ok := a.driverQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

func (a driverQueueBackendAdapter) Resume(ctx context.Context, queueName string) error {
	controller, ok := a.driverQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

func (a driverQueueBackendAdapter) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := a.driverQueueBackend.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", a.Driver())
	}
	return provider.Stats(ctx)
}

func (a driverQueueBackendAdapter) Ready(ctx context.Context) error {
	return runtimeReadyCheck(ctx, a.driverQueueBackend)
}

func (a driverQueueBackendAdapter) ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	return admin.ListJobs(ctx, opts)
}

func (a driverQueueBackendAdapter) RetryJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, queueName, jobID)
}

func (a driverQueueBackendAdapter) CancelJob(ctx context.Context, jobID string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

func (a driverQueueBackendAdapter) DeleteJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, queueName, jobID)
}

func (a driverQueueBackendAdapter) ClearQueue(ctx context.Context, queueName string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, queueName)
}

func (a driverQueueBackendAdapter) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return nil, ErrQueueAdminUnsupported
	}
	return admin.History(ctx, queueName, window)
}

func (a driverRuntimeQueueBackendAdapter) Pause(ctx context.Context, queueName string) error {
	controller, ok := a.driverRuntimeQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

func (a driverRuntimeQueueBackendAdapter) Resume(ctx context.Context, queueName string) error {
	controller, ok := a.driverRuntimeQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

func (a driverRuntimeQueueBackendAdapter) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := a.driverRuntimeQueueBackend.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", a.Driver())
	}
	return provider.Stats(ctx)
}

func (a driverRuntimeQueueBackendAdapter) Ready(ctx context.Context) error {
	return runtimeReadyCheck(ctx, a.driverRuntimeQueueBackend)
}

func (a driverRuntimeQueueBackendAdapter) ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	return admin.ListJobs(ctx, opts)
}

func (a driverRuntimeQueueBackendAdapter) RetryJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, queueName, jobID)
}

func (a driverRuntimeQueueBackendAdapter) CancelJob(ctx context.Context, jobID string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

func (a driverRuntimeQueueBackendAdapter) DeleteJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, queueName, jobID)
}

func (a driverRuntimeQueueBackendAdapter) ClearQueue(ctx context.Context, queueName string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, queueName)
}

func (a driverRuntimeQueueBackendAdapter) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return nil, ErrQueueAdminUnsupported
	}
	return admin.History(ctx, queueName, window)
}

func runtimeReadyCheck(ctx context.Context, raw any) error {
	if checker, ok := raw.(interface{ Ready(context.Context) error }); ok {
		return checker.Ready(ctx)
	}
	// Backward-compatible bridge for older backend implementations.
	if checker, ok := raw.(interface{ Preflight(context.Context) error }); ok {
		return checker.Preflight(ctx)
	}
	return nil
}

func optionalDriverMovedError(driver Driver) error {
	switch driver {
	case DriverRedis:
		return fmt.Errorf("redis driver moved; use github.com/goforj/queue/driver/redisqueue")
	case DriverNATS:
		return fmt.Errorf("nats driver moved; use github.com/goforj/queue/driver/natsqueue")
	case DriverSQS:
		return fmt.Errorf("sqs driver moved; use github.com/goforj/queue/driver/sqsqueue")
	case DriverRabbitMQ:
		return fmt.Errorf("rabbitmq driver moved; use github.com/goforj/queue/driver/rabbitmqqueue")
	case DriverDatabase:
		return fmt.Errorf("database drivers moved; use github.com/goforj/queue/driver/{mysqlqueue,postgresqueue,sqlitequeue}")
	default:
		return fmt.Errorf("unsupported queue driver %q", driver)
	}
}

func (q *queueCommon) jobFromAny(job any) (Job, error) {
	if job, ok := job.(Job); ok {
		if job.Type == "" {
			return Job{}, fmt.Errorf("dispatch job type is required")
		}
		return job, nil
	}
	if job == nil {
		return Job{}, fmt.Errorf("dispatch job is nil")
	}
	jobType := jobTypeFromValue(job)
	if jobType == "" {
		return Job{}, fmt.Errorf("dispatch job type could not be inferred")
	}
	if marshaler, ok := job.(interface{ JobType() string }); ok {
		if t := marshaler.JobType(); t != "" {
			jobType = t
		}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return Job{}, fmt.Errorf("marshal dispatch job: %w", err)
	}
	return NewJob(jobType).Payload(payload).OnQueue(q.cfg.DefaultQueue), nil
}

func jobTypeFromValue(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return ""
	}
	return t.Name()
}
