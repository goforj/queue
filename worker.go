package queue

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
)

type workerRuntime interface {
	Driver() Driver
	Register(taskType string, handler Handler)
	Start() error
	Shutdown() error
}

// workerConfig configures internal worker runtime creation.
// @group Config
type workerConfig struct {
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

func newWorkerFromConfig(cfg workerConfig) (workerRuntime, error) {
	var worker workerRuntime
	var err error
	switch cfg.Driver {
	case DriverSync:
		q := newSyncQueue()
		worker = &queueWorkerAdapter{
			runtime: q.(interface {
				Register(string, Handler)
				StartWorkers(context.Context) error
				Shutdown(context.Context) error
			}),
			driver: DriverSync,
			dispatch: func(ctx context.Context, task Task) error {
				return q.Dispatch(ctx, task)
			},
		}
	case DriverWorkerpool:
		q := newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{
			Workers:            cfg.Workers,
			QueueCapacity:      cfg.QueueCapacity,
			DefaultTaskTimeout: cfg.DefaultTaskTimeout,
		}.normalize())
		worker = &queueWorkerAdapter{
			runtime: q,
			driver:  DriverWorkerpool,
			dispatch: func(ctx context.Context, task Task) error {
				return q.Dispatch(ctx, task)
			},
		}
	case DriverDatabase:
		autoMigrate := cfg.AutoMigrate
		if !autoMigrate {
			autoMigrate = true
		}
		var d queueBackend
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
		worker = &queueWorkerAdapter{
			runtime: d.(interface {
				Register(string, Handler)
				StartWorkers(context.Context) error
				Shutdown(context.Context) error
			}),
			driver: DriverDatabase,
			dispatch: func(ctx context.Context, task Task) error {
				return d.Dispatch(ctx, task)
			},
		}
	case DriverRedis:
		if cfg.RedisAddr == "" {
			return nil, fmt.Errorf("redis addr is required")
		}
		concurrency := defaultWorkerCount(cfg.Workers)
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
	runtime interface {
		Register(taskType string, handler Handler)
		StartWorkers(ctx context.Context) error
		Shutdown(ctx context.Context) error
	}
	driver   Driver
	dispatch func(ctx context.Context, task Task) error
}

func (w *queueWorkerAdapter) Driver() Driver {
	return w.driver
}

func (w *queueWorkerAdapter) Register(taskType string, handler Handler) {
	w.runtime.Register(taskType, handler)
}

func (w *queueWorkerAdapter) Start() error {
	return w.runtime.StartWorkers(context.Background())
}

func (w *queueWorkerAdapter) Shutdown() error {
	return w.runtime.Shutdown(context.Background())
}
