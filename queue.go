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
	Driver() Driver

	// Dispatch submits a typed job payload using the default queue.
	Dispatch(job any) error

	// DispatchCtx submits a typed job payload using the provided context.
	DispatchCtx(ctx context.Context, job any) error

	// Register associates a handler with a task type.
	Register(taskType string, handler Handler)

	// StartWorkers starts worker execution.
	StartWorkers(ctx context.Context) error

	// Workers sets desired worker concurrency before StartWorkers.
	Workers(count int) Queue

	// Shutdown drains running work and releases resources.
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
	Dispatch(ctx context.Context, task Task) error
	Shutdown(ctx context.Context) error
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
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
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
//		queue.NewTask("emails:send").
//			Payload(EmailPayload{ID: 1}).
//			OnQueue("default"),
//	)
func New(cfg Config) (Queue, error) {
	var q queueBackend
	var err error
	switch cfg.Driver {
	case DriverSync:
		q = newSyncQueue()
	case DriverWorkerpool:
		q = newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{})
	case DriverRedis:
		if cfg.RedisAddr == "" {
			return nil, fmt.Errorf("redis addr is required")
		}
		q = newRedisQueue(newAsynqClient(cfg), newAsynqInspector(cfg), true)
	case DriverDatabase:
		q, err = newDatabaseQueue(cfg.databaseConfig())
	case DriverNATS:
		if cfg.NATSURL == "" {
			return nil, fmt.Errorf("nats url is required")
		}
		q = newNATSQueue(cfg.NATSURL)
	case DriverSQS:
		if cfg.SQSRegion == "" {
			cfg.SQSRegion = "us-east-1"
		}
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
	return &queueRuntime{
		inner:      newObservedQueue(q, cfg.Observer),
		cfg:        cfg,
		registered: make(map[string]Handler),
	}, nil
}

type queueRuntime struct {
	inner queueBackend
	cfg   Config
	mu    sync.Mutex
	// registered tracks handlers for queue-centric registration + worker start.
	registered map[string]Handler
	worker     workerRuntime
	started    bool
	workers    int
}

type registerStartShutdown interface {
	Register(taskType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

func (q *queueRuntime) Driver() Driver {
	if driverAware, ok := q.inner.(interface{ Driver() Driver }); ok {
		return driverAware.Driver()
	}
	return q.cfg.Driver
}

func (q *queueRuntime) Dispatch(job any) error {
	return q.DispatchCtx(context.Background(), job)
}

func (q *queueRuntime) DispatchCtx(ctx context.Context, job any) error {
	task, err := q.taskFromJob(job)
	if err != nil {
		return err
	}
	return q.inner.Dispatch(ctx, task)
}

func (q *queueRuntime) Register(taskType string, handler Handler) {
	q.mu.Lock()
	if q.registered == nil {
		q.registered = make(map[string]Handler)
	}
	q.registered[taskType] = handler
	w := q.worker
	started := q.started
	q.mu.Unlock()

	// Runtime-capable backends register directly on the backend.
	if runtime, ok := q.runtimeBackend(); ok {
		runtime.Register(taskType, handler)
		return
	}
	// External worker backends register on active worker after start.
	if started && w != nil {
		w.Register(taskType, handler)
	}
}

func (q *queueRuntime) StartWorkers(ctx context.Context) error {
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

	if runtime, ok := q.runtimeBackend(); ok {
		for taskType, handler := range registered {
			runtime.Register(taskType, handler)
		}
		if err := runtime.StartWorkers(ctx); err != nil {
			return err
		}
		q.mu.Lock()
		q.started = true
		q.mu.Unlock()
		return nil
	}

	w, err := q.newExternalWorker(workers)
	if err != nil {
		return err
	}
	for taskType, handler := range registered {
		w.Register(taskType, handler)
	}
	if err := w.Start(); err != nil {
		return err
	}
	q.mu.Lock()
	q.worker = w
	q.started = true
	q.mu.Unlock()
	return nil
}

func (q *queueRuntime) Workers(count int) Queue {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && count > 0 {
		q.workers = count
	}
	return q
}

func (q *queueRuntime) Shutdown(ctx context.Context) error {
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
		if runtime, ok := q.runtimeBackend(); ok {
			if err := runtime.Shutdown(ctx); err != nil {
				return err
			}
			return nil
		}
		if w != nil {
			if err := w.Shutdown(); err != nil {
				return err
			}
		}
	}
	return q.inner.Shutdown(ctx)
}

func (q *queueRuntime) Pause(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

func (q *queueRuntime) Resume(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

func (q *queueRuntime) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := q.inner.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", q.Driver())
	}
	return provider.Stats(ctx)
}

func (q *queueRuntime) runtimeBackend() (registerStartShutdown, bool) {
	// Observability wrappers always expose StartWorkers/Register methods, but
	// only some underlying backends are true runtime backends.
	if observed, ok := q.inner.(*observedQueue); ok {
		if _, ok := observed.inner.(registerStartShutdown); ok {
			return observed, true
		}
		return nil, false
	}
	runtime, ok := q.inner.(registerStartShutdown)
	return runtime, ok
}

func (q *queueRuntime) newExternalWorker(concurrency int) (workerRuntime, error) {
	switch q.cfg.Driver {
	case DriverRedis:
		workers := concurrency
		workers = defaultWorkerCount(workers)
		return newObservedWorker(
			newRedisWorker(
				asynq.NewServer(asynq.RedisClientOpt{
					Addr:     q.cfg.RedisAddr,
					Password: q.cfg.RedisPassword,
					DB:       q.cfg.RedisDB,
				}, asynq.Config{Concurrency: workers}),
				asynq.NewServeMux(),
			),
			q.cfg.Observer,
		), nil
	case DriverNATS:
		return newObservedWorker(newNATSWorker(q.cfg.NATSURL), q.cfg.Observer), nil
	case DriverSQS:
		cfg := q.cfg
		if cfg.SQSRegion == "" {
			cfg.SQSRegion = "us-east-1"
		}
		return newObservedWorker(newSQSWorker(workerConfig{
			Driver:       DriverSQS,
			Observer:     cfg.Observer,
			Workers:      concurrency,
			DefaultQueue: cfg.DefaultQueue,
			SQSRegion:    cfg.SQSRegion,
			SQSEndpoint:  cfg.SQSEndpoint,
			SQSAccessKey: cfg.SQSAccessKey,
			SQSSecretKey: cfg.SQSSecretKey,
		}), q.cfg.Observer), nil
	case DriverRabbitMQ:
		return newObservedWorker(newRabbitMQWorker(workerConfig{
			Driver:       DriverRabbitMQ,
			Observer:     q.cfg.Observer,
			Workers:      concurrency,
			DefaultQueue: q.cfg.DefaultQueue,
			RabbitMQURL:  q.cfg.RabbitMQURL,
		}), q.cfg.Observer), nil
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", q.cfg.Driver)
	}
}

// NewQueueWithDefaults creates a queue runtime and sets the default queue name.
func NewQueueWithDefaults(defaultQueue string, cfg Config) (Queue, error) {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = defaultQueue
	}
	return New(cfg)
}

func (q *queueRuntime) taskFromJob(job any) (Task, error) {
	if task, ok := job.(Task); ok {
		if task.Type == "" {
			return Task{}, fmt.Errorf("dispatch task type is required")
		}
		return task, nil
	}
	if job == nil {
		return Task{}, fmt.Errorf("dispatch job is nil")
	}
	taskType := taskTypeFromValue(job)
	if taskType == "" {
		return Task{}, fmt.Errorf("dispatch job type could not be inferred")
	}
	if marshaler, ok := job.(interface{ TaskType() string }); ok {
		if t := marshaler.TaskType(); t != "" {
			taskType = t
		}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return Task{}, fmt.Errorf("marshal dispatch job: %w", err)
	}
	queueName := q.cfg.DefaultQueue
	if queueName == "" {
		queueName = "default"
	}
	return NewTask(taskType).Payload(payload).OnQueue(queueName), nil
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
