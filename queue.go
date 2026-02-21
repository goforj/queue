package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/hibiken/asynq"
)

// Queue is the queue abstraction exposed to callers.
type Queue interface {
	// Driver returns the active queue driver.
	// @group Queue
	//
	// Example: inspect queue driver
	//
	//	var q queue.Queue
	//	driver := q.Driver()
	//	_ = driver
	Driver() Driver

	// Dispatch submits a typed job payload using the default queue.
	// @group Queue
	//
	// Example: dispatch typed task
	//
	//	var q queue.Queue
	//	err := q.Dispatch(
	//		queue.NewJob("emails:send").
	//			Payload(map[string]any{"id": 1}).
	//			OnQueue("default"),
	//	)
	//	_ = err
	Dispatch(job any) error

	// DispatchCtx submits a typed job payload using the provided context.
	// @group Queue
	//
	// Example: dispatch with context
	//
	//	var q queue.Queue
	//	err := q.DispatchCtx(
	//		context.Background(),
	//		queue.NewJob("emails:send").OnQueue("default"),
	//	)
	//	_ = err
	DispatchCtx(ctx context.Context, job any) error

	// Register associates a handler with a task type.
	// @group Queue
	//
	// Example: register a handler
	//
	//	var q queue.Queue
	//	q.Register("emails:send", func(context.Context, queue.Job) error { return nil })
	Register(taskType string, handler Handler)

	// StartWorkers starts worker execution.
	// @group Queue
	//
	// Example: start workers
	//
	//	var q queue.Queue
	//	err := q.StartWorkers(context.Background())
	//	_ = err
	StartWorkers(ctx context.Context) error

	// Workers sets desired worker concurrency before StartWorkers.
	// @group Queue
	//
	// Example: set worker count
	//
	//	var q queue.Queue
	//	q = q.Workers(4)
	Workers(count int) Queue

	// Shutdown drains running work and releases resources.
	// @group Queue
	//
	// Example: shutdown runtime
	//
	//	var q queue.Queue
	//	err := q.Shutdown(context.Background())
	//	_ = err
	Shutdown(ctx context.Context) error
}

// WorkerpoolConfig configures the in-memory workerpool q.
// @group Config
type WorkerpoolConfig struct {
	Workers            int
	QueueCapacity      int
	DefaultTaskTimeout time.Duration
}

func (c WorkerpoolConfig) normalize() WorkerpoolConfig {
	c.Workers = defaultWorkerCount(c.Workers)
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = c.Workers
	}
	return c
}

// Config configures queue runtime creation for New.
// @group Config
type Config struct {
	Driver   Driver
	Observer Observer

	DefaultQueue string

	Database       *sql.DB
	DatabaseDriver string
	DatabaseDSN    string

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
	Dispatch(ctx context.Context, task Job) error
	Shutdown(ctx context.Context) error
}

type runtimeQueueBackend interface {
	queueBackend
	Register(taskType string, handler Handler)
	StartWorkers(ctx context.Context) error
}

func newSyncQueue() queueBackend {
	return newLocalQueueWithConfig(DriverSync, WorkerpoolConfig{})
}

func (cfg Config) databaseConfig() DatabaseConfig {
	return DatabaseConfig{
		DB:           cfg.Database,
		DriverName:   cfg.DatabaseDriver,
		DSN:          cfg.DatabaseDSN,
		DefaultQueue: cfg.DefaultQueue,
	}
}

// New creates a queue based on Config.Driver.
// @group Constructors
//
// Example: new queue from config
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, task queue.Job) error {
//		var payload EmailPayload
//		if err := task.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	_ = q.Workers(1).StartWorkers(context.Background())
//	defer q.Shutdown(context.Background())
//	_ = q.DispatchCtx(
//		context.Background(),
//		queue.NewJob("emails:send").
//			Payload(EmailPayload{ID: 1}).
//			OnQueue("default"),
//	)
func New(cfg Config) (Queue, error) {
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

// NewNull creates a drop-only queue runtime.
// @group Constructors
//
// Example: new null queue
//
//	q, err := queue.NewNull()
//	if err != nil {
//		return
//	}
//	_ = q.Dispatch(queue.NewJob("emails:send").Payload(map[string]int{"id": 1}).OnQueue("default"))
func NewNull() (Queue, error) {
	return New(Config{Driver: DriverNull})
}

// NewSync creates a synchronous in-process queue runtime.
// @group Constructors
//
// Example: new sync queue
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	_ = q
func NewSync() (Queue, error) {
	return New(Config{Driver: DriverSync})
}

// NewWorkerpool creates an in-process workerpool queue runtime.
// @group Constructors
//
// Example: new workerpool queue
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	_ = q
func NewWorkerpool() (Queue, error) {
	return New(Config{Driver: DriverWorkerpool})
}

// NewDatabase creates a SQL-backed queue runtime.
// @group Constructors
//
// Example: new database queue
//
//	q, err := queue.NewDatabase("sqlite", "file:queue.db?_busy_timeout=5000")
//	if err != nil {
//		return
//	}
//	_ = q
func NewDatabase(driverName, dsn string) (Queue, error) {
	return New(Config{
		Driver:         DriverDatabase,
		DatabaseDriver: driverName,
		DatabaseDSN:    dsn,
	})
}

// NewRedis creates a Redis-backed queue runtime.
// @group Constructors
//
// Example: new redis queue
//
//	q, err := queue.NewRedis("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	_ = q
func NewRedis(addr string) (Queue, error) {
	return New(Config{
		Driver:    DriverRedis,
		RedisAddr: addr,
	})
}

// NewNATS creates a NATS-backed queue runtime.
// @group Constructors
//
// Example: new nats queue
//
//	q, err := queue.NewNATS("nats://127.0.0.1:4222")
//	if err != nil {
//		return
//	}
//	_ = q
func NewNATS(url string) (Queue, error) {
	return New(Config{
		Driver:  DriverNATS,
		NATSURL: url,
	})
}

// NewSQS creates an SQS-backed queue runtime.
// @group Constructors
//
// Example: new sqs queue
//
//	q, err := queue.NewSQS("us-east-1")
//	if err != nil {
//		return
//	}
//	_ = q
func NewSQS(region string) (Queue, error) {
	return New(Config{
		Driver:    DriverSQS,
		SQSRegion: region,
	})
}

// NewRabbitMQ creates a RabbitMQ-backed queue runtime.
// @group Constructors
//
// Example: new rabbitmq queue
//
//	q, err := queue.NewRabbitMQ("amqp://guest:guest@127.0.0.1:5672/")
//	if err != nil {
//		return
//	}
//	_ = q
func NewRabbitMQ(url string) (Queue, error) {
	return New(Config{
		Driver:      DriverRabbitMQ,
		RabbitMQURL: url,
	})
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
	Register(taskType string, handler Handler)
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
	task, err := q.taskFromJob(job)
	if err != nil {
		return err
	}
	return q.inner.Dispatch(ctx, task)
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

func (q *nativeQueueRuntime) Register(taskType string, handler Handler) {
	q.mu.Lock()
	if q.registered == nil {
		q.registered = make(map[string]Handler)
	}
	q.registered[taskType] = handler
	started := q.started
	q.mu.Unlock()

	if started {
		q.runtime.Register(taskType, q.common.wrapRegisteredHandler(taskType, handler))
	}
}

func (q *externalQueueRuntime) Register(taskType string, handler Handler) {
	q.mu.Lock()
	if q.registered == nil {
		q.registered = make(map[string]Handler)
	}
	q.registered[taskType] = handler
	w := q.worker
	started := q.started
	q.mu.Unlock()

	if started && w != nil {
		w.Register(taskType, q.common.wrapRegisteredHandler(taskType, handler))
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
	for taskType, handler := range q.registered {
		registered[taskType] = handler
	}
	q.mu.Unlock()

	for taskType, handler := range registered {
		q.runtime.Register(taskType, q.common.wrapRegisteredHandler(taskType, handler))
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
	for taskType, handler := range q.registered {
		registered[taskType] = handler
	}
	q.mu.Unlock()

	w, err := newExternalWorker(q.common.cfg, workers)
	if err != nil {
		return err
	}
	for taskType, handler := range registered {
		w.Register(taskType, q.common.wrapRegisteredHandler(taskType, handler))
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

func (q *nativeQueueRuntime) Workers(count int) Queue {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && count > 0 {
		q.workers = count
	}
	return q
}

func (q *externalQueueRuntime) Workers(count int) Queue {
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

func (q *queueCommon) wrapRegisteredHandler(taskType string, handler Handler) Handler {
	if handler == nil || q.cfg.Observer == nil {
		return handler
	}
	return wrapObservedHandler(q.cfg.Observer, q.cfg.Driver, "", taskType, handler)
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
		return newNATSWorker(cfg.NATSURL), nil
	case DriverSQS:
		return newSQSWorker(sqsWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			SQSRegion:    cfg.SQSRegion,
			SQSEndpoint:  cfg.SQSEndpoint,
			SQSAccessKey: cfg.SQSAccessKey,
			SQSSecretKey: cfg.SQSSecretKey,
		}), nil
	case DriverRabbitMQ:
		return newRabbitMQWorker(rabbitMQWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			RabbitMQURL:  cfg.RabbitMQURL,
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
//	q.Register("emails:send", func(ctx context.Context, task queue.Job) error {
//		var payload EmailPayload
//		if err := task.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	_ = q.StartWorkers(context.Background())
//	defer q.Shutdown(context.Background())
//	_ = q.Dispatch(queue.NewJob("emails:send").Payload(EmailPayload{ID: 1}))
func NewQueueWithDefaults(defaultQueue string, cfg Config) (Queue, error) {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = defaultQueue
	}
	return New(cfg)
}

func (q *queueCommon) taskFromJob(job any) (Job, error) {
	if task, ok := job.(Job); ok {
		if task.Type == "" {
			return Job{}, fmt.Errorf("dispatch task type is required")
		}
		return task, nil
	}
	if job == nil {
		return Job{}, fmt.Errorf("dispatch job is nil")
	}
	taskType := taskTypeFromValue(job)
	if taskType == "" {
		return Job{}, fmt.Errorf("dispatch job type could not be inferred")
	}
	if marshaler, ok := job.(interface{ JobType() string }); ok {
		if t := marshaler.JobType(); t != "" {
			taskType = t
		}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return Job{}, fmt.Errorf("marshal dispatch job: %w", err)
	}
	return NewJob(taskType).Payload(payload).OnQueue(q.cfg.DefaultQueue), nil
}

func taskTypeFromValue(v any) string {
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
