package queue

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"time"

	"github.com/hibiken/asynq"
)

// Worker processes queued tasks using registered handlers.
// @group Worker
//
// Example: worker lifecycle
//
//	worker, err := queue.NewWorker(queue.WorkerConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		var payload EmailPayload
//		if err := task.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	_ = worker.Start()
//	_ = worker.Shutdown()
type Worker interface {
	Driver() Driver
	Register(taskType string, handler Handler)
	Start() error
	Shutdown() error
}

// WorkerConfig configures worker creation for NewWorker.
// @group Config
type WorkerConfig struct {
	Driver   Driver
	Observer Observer

	Workers            int
	QueueCapacity      int
	DefaultTaskTimeout time.Duration

	PollInterval time.Duration
	DefaultQueue string
	AutoMigrate  bool

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

// NewWorker creates a worker based on WorkerConfig.Driver.
// @group Constructors
//
// Example: new sync worker
//
//	worker, err := queue.NewWorker(queue.WorkerConfig{
//		Driver: queue.DriverSync,
//	})
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		var payload EmailPayload
//		if err := task.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	_ = worker.Start()
//	_ = worker.Shutdown()
func NewWorker(cfg WorkerConfig) (Worker, error) {
	var worker Worker
	var err error
	switch cfg.Driver {
	case DriverSync:
		worker = &queueWorkerAdapter{q: newSyncQueue(), driver: DriverSync}
	case DriverWorkerpool:
		worker = &queueWorkerAdapter{
			q: newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{
				Workers:            cfg.Workers,
				QueueCapacity:      cfg.QueueCapacity,
				DefaultTaskTimeout: cfg.DefaultTaskTimeout,
			}.normalize()),
			driver: DriverWorkerpool,
		}
	case DriverDatabase:
		autoMigrate := cfg.AutoMigrate
		if !autoMigrate {
			autoMigrate = true
		}
		var d Queue
		d, err = newDatabaseQueue(DatabaseConfig{
			DB:           cfg.Database,
			DriverName:   cfg.DatabaseDriver,
			DSN:          cfg.DatabaseDSN,
			Workers:      cfg.Workers,
			PollInterval: cfg.PollInterval,
			DefaultQueue: cfg.DefaultQueue,
			AutoMigrate:  autoMigrate,
		})
		if err != nil {
			return nil, err
		}
		worker = &queueWorkerAdapter{q: d, driver: DriverDatabase}
	case DriverRedis:
		if cfg.RedisAddr == "" {
			return nil, fmt.Errorf("redis addr is required")
		}
		concurrency := cfg.Workers
		if concurrency <= 0 {
			concurrency = runtime.NumCPU()
		}
		if concurrency <= 0 {
			concurrency = 1
		}
		worker = newRedisWorker(
			asynq.NewServer(asynq.RedisClientOpt{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
			}, asynq.Config{Concurrency: concurrency}),
			asynq.NewServeMux(),
		)
	case DriverNATS:
		if cfg.NATSURL == "" {
			return nil, fmt.Errorf("nats url is required")
		}
		worker = newNATSWorker(cfg.NATSURL)
	case DriverSQS:
		if cfg.SQSRegion == "" {
			cfg.SQSRegion = "us-east-1"
		}
		worker = newSQSWorker(cfg)
	case DriverRabbitMQ:
		if cfg.RabbitMQURL == "" {
			return nil, fmt.Errorf("rabbitmq url is required")
		}
		worker = newRabbitMQWorker(cfg)
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
	return newObservedWorker(worker, cfg.Observer), nil
}

type queueWorkerAdapter struct {
	q      Queue
	driver Driver
}

func (w *queueWorkerAdapter) Driver() Driver {
	return w.driver
}

func (w *queueWorkerAdapter) Register(taskType string, handler Handler) {
	w.q.Register(taskType, handler)
}

func (w *queueWorkerAdapter) Start() error {
	return w.q.Start(context.Background())
}

func (w *queueWorkerAdapter) Shutdown() error {
	return w.q.Shutdown(context.Background())
}
