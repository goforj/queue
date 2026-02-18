package queue

import (
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
	Driver Driver

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
	switch cfg.Driver {
	case DriverSync:
		return &queueWorkerAdapter{q: newSyncQueue(), driver: DriverSync}, nil
	case DriverWorkerpool:
		return &queueWorkerAdapter{
			q: newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{
				Workers:            cfg.Workers,
				QueueCapacity:      cfg.QueueCapacity,
				DefaultTaskTimeout: cfg.DefaultTaskTimeout,
			}.normalize()),
			driver: DriverWorkerpool,
		}, nil
	case DriverDatabase:
		autoMigrate := cfg.AutoMigrate
		if !autoMigrate {
			autoMigrate = true
		}
		d, err := newDatabaseQueue(DatabaseConfig{
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
		return &queueWorkerAdapter{q: d, driver: DriverDatabase}, nil
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
		return newRedisWorker(
			asynq.NewServer(asynq.RedisClientOpt{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
			}, asynq.Config{Concurrency: concurrency}),
			asynq.NewServeMux(),
		), nil
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
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
	return w.q.Start(nil)
}

func (w *queueWorkerAdapter) Shutdown() error {
	return w.q.Shutdown(nil)
}
