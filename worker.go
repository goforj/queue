package queue

import (
	"fmt"
	"runtime"

	"github.com/hibiken/asynq"
)

// Worker processes queued tasks using registered handlers.
// @group Worker
//
// Example: worker lifecycle
//
//	worker, err := queue.NewWorker(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
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

// NewWorker creates a worker based on Config.Driver.
// @group Constructors
//
// Example: new sync worker
//
//	worker, err := queue.NewWorker(queue.Config{
//		Driver: queue.DriverSync,
//	})
//	if err != nil {
//		return
//	}
//	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = worker.Start()
//	_ = worker.Shutdown()
func NewWorker(cfg Config) (Worker, error) {
	switch cfg.Driver {
	case DriverSync:
		return &dispatcherWorkerAdapter{dispatcher: newSyncDispatcher()}, nil
	case DriverWorkerpool:
		return &dispatcherWorkerAdapter{
			dispatcher: newLocalDispatcherWithConfig(DriverWorkerpool, cfg.workerpoolConfig().normalize()),
		}, nil
	case DriverDatabase:
		d, err := newDatabaseDispatcher(cfg.databaseConfig())
		if err != nil {
			return nil, err
		}
		return &dispatcherWorkerAdapter{dispatcher: d}, nil
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

type dispatcherWorkerAdapter struct {
	dispatcher Dispatcher
}

func (w *dispatcherWorkerAdapter) Driver() Driver {
	return w.dispatcher.Driver()
}

func (w *dispatcherWorkerAdapter) Register(taskType string, handler Handler) {
	w.dispatcher.Register(taskType, handler)
}

func (w *dispatcherWorkerAdapter) Start() error {
	return w.dispatcher.Start(nil)
}

func (w *dispatcherWorkerAdapter) Shutdown() error {
	return w.dispatcher.Shutdown(nil)
}
