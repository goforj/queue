package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/goforj/queue/internal/busruntime"
	"github.com/hibiken/asynq"
)

// QueueRuntime is the low-level queue runtime abstraction exposed to callers.
type QueueRuntime interface {
	// Driver returns the active queue driver.
	// @group Queue Runtime
	//
	// Example: inspect queue driver
	//
	//	var q queue.QueueRuntime
	//	driver := q.Driver()
	//	_ = driver
	Driver() Driver

	// Dispatch submits a typed job payload using the default queue.
	// @group Queue Runtime
	//
	// Example: dispatch typed job
	//
	//	var q queue.QueueRuntime
	//	err := q.Dispatch(
	//		queue.NewJob("emails:send").
	//			Payload(map[string]any{"id": 1}).
	//			OnQueue("default"),
	//	)
	//	_ = err
	Dispatch(job any) error

	// DispatchCtx submits a typed job payload using the provided context.
	// @group Queue Runtime
	//
	// Example: dispatch with context
	//
	//	var q queue.QueueRuntime
	//	err := q.DispatchCtx(
	//		context.Background(),
	//		queue.NewJob("emails:send").OnQueue("default"),
	//	)
	//	_ = err
	DispatchCtx(ctx context.Context, job any) error

	// Register associates a handler with a job type.
	// @group Queue Runtime
	//
	// Example: register a handler
	//
	//	var q queue.QueueRuntime
	//	q.Register("emails:send", func(context.Context, queue.Job) error { return nil })
	Register(jobType string, handler Handler)

	// StartWorkers starts worker execution.
	// @group Queue Runtime
	//
	// Example: start workers
	//
	//	var q queue.QueueRuntime
	//	err := q.StartWorkers(context.Background())
	//	_ = err
	StartWorkers(ctx context.Context) error

	// Workers sets desired worker concurrency before StartWorkers.
	// @group Queue Runtime
	//
	// Example: set worker count
	//
	//	var q queue.QueueRuntime
	//	q = q.Workers(4)
	Workers(count int) QueueRuntime

	// Shutdown drains running work and releases resources.
	// @group Queue Runtime
	//
	// Example: shutdown runtime
	//
	//	var q queue.QueueRuntime
	//	err := q.Shutdown(context.Background())
	//	_ = err
	Shutdown(ctx context.Context) error
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

// Config configures queue creation for New and NewQueue.
// @group Config
type Config struct {
	Driver   Driver
	Observer Observer

	DefaultQueue string

	Database                         *sql.DB
	DatabaseDriver                   string
	DatabaseDSN                      string
	DatabaseProcessingRecoveryGrace  time.Duration
	DatabaseProcessingLeaseNoTimeout time.Duration

	RedisAddr     string
	RedisPassword string
	RedisDB       int

	NATSURL string

	SQSRegion    string
	SQSEndpoint  string
	SQSAccessKey string
	SQSSecretKey string

	RabbitMQURL string
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

func (cfg Config) databaseConfig() DatabaseConfig {
	return DatabaseConfig{
		DB:                       cfg.Database,
		DriverName:               cfg.DatabaseDriver,
		DSN:                      cfg.DatabaseDSN,
		DefaultQueue:             cfg.DefaultQueue,
		ProcessingRecoveryGrace:  cfg.DatabaseProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.DatabaseProcessingLeaseNoTimeout,
		Observer:                 cfg.Observer,
	}
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
//	q.Register("emails:send", func(ctx context.Context, jc queue.Context) error {
//		var payload EmailPayload
//		if err := jc.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	_ = q.Workers(1).StartWorkers(context.Background())
//	defer q.Shutdown(context.Background())
//	_, _ = q.Dispatch(
//		context.Background(),
//		queue.NewJob("emails:send").
//			Payload(EmailPayload{ID: 1}).
//			OnQueue("default"),
//	)
func New(cfg Config, opts ...RuntimeOption) (*Queue, error) {
	return newHighLevelQueue(cfg, opts...)
}

// NewQueue creates the low-level queue runtime (driver-facing API) based on Config.Driver.
// Use this only for driver-focused/advanced runtime access; application code should prefer New.
// @group Constructors
func NewQueue(cfg Config) (QueueRuntime, error) {
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
		q, err = newDatabaseQueue(cfg.databaseConfig())
	case DriverRedis:
		if cfg.RedisAddr == "" {
			return nil, fmt.Errorf("redis addr is required")
		}
		q = newRedisQueue(newAsynqClient(cfg), newAsynqInspector(cfg), true)
	case DriverNATS:
		if cfg.NATSURL == "" {
			return nil, fmt.Errorf("nats url is required")
		}
		q = newNATSQueue(cfg.NATSURL)
	case DriverSQS:
		q = newSQSQueue(cfg)
	case DriverRabbitMQ:
		if cfg.RabbitMQURL == "" {
			return nil, fmt.Errorf("rabbitmq url is required")
		}
		q = newRabbitMQQueue(cfg.RabbitMQURL, cfg.DefaultQueue)
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
	if cfg.SQSRegion == "" {
		cfg.SQSRegion = "us-east-1"
	}
	return cfg
}

type queueCommon struct {
	inner  queueBackend
	cfg    Config
	driver Driver
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
}

type runtimeWorkerBackend interface {
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

func (q *queueCommon) Driver() Driver {
	return q.driver
}

func (q *queueCommon) Dispatch(job any) error {
	return q.DispatchCtx(context.Background(), job)
}

func (q *queueCommon) DispatchCtx(ctx context.Context, job any) error {
	dispatchJob, err := q.jobFromAny(job)
	if err != nil {
		return err
	}
	return q.inner.Dispatch(ctx, dispatchJob)
}

func (q *nativeQueueRuntime) Driver() Driver         { return q.common.Driver() }
func (q *nativeQueueRuntime) Dispatch(job any) error { return q.common.Dispatch(job) }
func (q *nativeQueueRuntime) DispatchCtx(ctx context.Context, job any) error {
	return q.common.DispatchCtx(ctx, job)
}

func (q *externalQueueRuntime) Driver() Driver         { return q.common.Driver() }
func (q *externalQueueRuntime) Dispatch(job any) error { return q.common.Dispatch(job) }
func (q *externalQueueRuntime) DispatchCtx(ctx context.Context, job any) error {
	return q.common.DispatchCtx(ctx, job)
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

	w, err := newExternalWorker(q.common.cfg, workers)
	if err != nil {
		return err
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

func (q *nativeQueueRuntime) Workers(count int) QueueRuntime {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && count > 0 {
		q.workers = count
	}
	return q
}

func (q *externalQueueRuntime) Workers(count int) QueueRuntime {
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

func (q *nativeQueueRuntime) Pause(ctx context.Context, queueName string) error {
	return q.common.Pause(ctx, queueName)
}
func (q *nativeQueueRuntime) Resume(ctx context.Context, queueName string) error {
	return q.common.Resume(ctx, queueName)
}
func (q *nativeQueueRuntime) Stats(ctx context.Context) (StatsSnapshot, error) {
	return q.common.Stats(ctx)
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

func (q *queueCommon) wrapRegisteredHandler(jobType string, handler Handler) Handler {
	if handler == nil || q.cfg.Observer == nil {
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
	case DriverRedis:
		workers := concurrency
		workers = defaultWorkerCount(workers)
		return newRedisWorker(
			asynq.NewServer(asynq.RedisClientOpt{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
			}, asynq.Config{Concurrency: workers}),
			asynq.NewServeMux(),
		), nil
	case DriverNATS:
		return newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      cfg.NATSURL,
			Workers:  defaultWorkerCount(concurrency),
			Observer: cfg.Observer,
		}), nil
	case DriverSQS:
		return newSQSWorker(sqsWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			SQSRegion:    cfg.SQSRegion,
			SQSEndpoint:  cfg.SQSEndpoint,
			SQSAccessKey: cfg.SQSAccessKey,
			SQSSecretKey: cfg.SQSSecretKey,
			Workers:      defaultWorkerCount(concurrency),
			Observer:     cfg.Observer,
		}), nil
	case DriverRabbitMQ:
		return newRabbitMQWorker(rabbitMQWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			RabbitMQURL:  cfg.RabbitMQURL,
			Workers:      defaultWorkerCount(concurrency),
			Observer:     cfg.Observer,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}

// NewQueueWithDefaults creates a queue runtime and sets the default queue name.
// @group Constructors
//
// Example: new queue with default queue
//
//	q, err := queue.NewQueueWithDefaults("critical", queue.Config{
//		Driver: queue.DriverSync,
//	})
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, job queue.Job) error {
//		var payload EmailPayload
//		if err := job.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	_ = q.StartWorkers(context.Background())
//	defer q.Shutdown(context.Background())
//	_ = q.Dispatch(queue.NewJob("emails:send").Payload(EmailPayload{ID: 1}))
func NewQueueWithDefaults(defaultQueue string, cfg Config) (QueueRuntime, error) {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = defaultQueue
	}
	return NewQueue(cfg)
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
