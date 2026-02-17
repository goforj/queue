package queue

import "github.com/hibiken/asynq"

// Worker processes queued tasks using registered handlers.
// @group Worker
//
// Example: worker lifecycle
//
//	worker := queue.NewSyncWorker()
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

// NewSyncWorker creates a synchronous in-process worker.
// @group Constructors
//
// Example: new sync worker
//
//	worker := queue.NewSyncWorker()
//	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = worker.Start()
//	_ = worker.Shutdown()
func NewSyncWorker() Worker {
	return &dispatcherWorkerAdapter{dispatcher: NewSyncDispatcher()}
}

// NewWorkerpoolWorker creates an in-process asynchronous workerpool worker.
// @group Constructors
//
// Example: new workerpool worker
//
//	worker := queue.NewWorkerpoolWorker(queue.WorkerpoolConfig{Workers: 2, Buffer: 16})
//	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = worker.Start()
//	_ = worker.Shutdown()
func NewWorkerpoolWorker(cfg WorkerpoolConfig) Worker {
	return &dispatcherWorkerAdapter{dispatcher: NewWorkerpoolDispatcher(cfg)}
}

// NewDatabaseWorker creates a durable SQL-backed worker.
// @group Constructors
//
// Example: new database worker
//
//	worker, err := queue.NewDatabaseWorker(queue.DatabaseConfig{
//		DriverName: "sqlite",
//		DSN:        "file:queue-worker.db?_busy_timeout=5000",
//		Workers:    1,
//	})
//	if err != nil {
//		return
//	}
//	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = worker.Start()
//	_ = worker.Shutdown()
func NewDatabaseWorker(cfg DatabaseConfig) (Worker, error) {
	d, err := NewDatabaseDispatcher(cfg)
	if err != nil {
		return nil, err
	}
	return &dispatcherWorkerAdapter{dispatcher: d}, nil
}

// NewRedisWorker creates a Redis-backed worker without exposing asynq handler types.
// @group Constructors
//
// Example: new redis worker constructor
//
//	_ = queue.NewRedisWorker
func NewRedisWorker(redis asynq.RedisConnOpt, cfg asynq.Config) Worker {
	return newRedisWorker(asynq.NewServer(redis, cfg), asynq.NewServeMux())
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
