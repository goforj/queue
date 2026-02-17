package queue

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/hibiken/asynq"
)

// Dispatcher is the queue abstraction exposed to callers.
type Dispatcher interface {
	// Driver returns the active backend driver.
	Driver() Driver

	// Start initializes background resources for drivers that need them.
	Start(ctx context.Context) error

	// Enqueue submits a task for execution with optional enqueue options.
	Enqueue(ctx context.Context, task Task, opts ...Option) error

	// Register associates a handler with a task type.
	Register(taskType string, handler Handler)

	// Shutdown drains running work and releases resources.
	Shutdown(ctx context.Context) error
}

// WorkerpoolConfig configures the in-memory workerpool dispatcher.
// @group Config
type WorkerpoolConfig struct {
	Workers     int
	Buffer      int
	TaskTimeout time.Duration
}

func (c WorkerpoolConfig) normalize() WorkerpoolConfig {
	if c.Workers <= 0 {
		c.Workers = runtime.NumCPU()
	}
	if c.Workers <= 0 {
		c.Workers = 1
	}
	if c.Buffer <= 0 {
		c.Buffer = c.Workers
	}
	return c
}

// Config configures dispatcher creation for NewDispatcher.
// @group Config
type Config struct {
	Driver     Driver
	Workerpool WorkerpoolConfig
	Database   DatabaseConfig
}

// RedisEnqueuer is the minimal enqueue dependency used by the redis dispatcher.
// @group Integration
type RedisEnqueuer interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// NewSyncDispatcher creates a synchronous in-process dispatcher.
// @group Constructors
//
// Example: new sync dispatcher
//
//	dispatcher := queue.NewSyncDispatcher()
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
func NewSyncDispatcher() Dispatcher {
	return newLocalDispatcherWithConfig(DriverSync, WorkerpoolConfig{})
}

// NewWorkerpoolDispatcher creates an in-memory asynchronous workerpool dispatcher.
// @group Constructors
//
// Example: new workerpool dispatcher
//
//	dispatcher := queue.NewWorkerpoolDispatcher(queue.WorkerpoolConfig{Workers: 2, Buffer: 16})
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = dispatcher.Start(context.Background())
//	_ = dispatcher.Shutdown(context.Background())
func NewWorkerpoolDispatcher(cfg WorkerpoolConfig) Dispatcher {
	return newLocalDispatcherWithConfig(DriverWorkerpool, cfg.normalize())
}

// NewRedisDispatcher creates a redis-backed dispatcher using an asynq-compatible enqueuer.
// @group Constructors
//
// Example: new redis dispatcher
//
//	dispatcher := queue.NewRedisDispatcher(nil)
//	fmt.Println(dispatcher.Driver())
func NewRedisDispatcher(client RedisEnqueuer) Dispatcher {
	return newRedisDispatcher(client)
}

// NewDispatcher creates a dispatcher based on Config.Driver.
// @group Constructors
//
// Example: new dispatcher from config
//
//	dispatcher, err := queue.NewDispatcher(queue.Config{Driver: queue.DriverSync}, nil)
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
func NewDispatcher(cfg Config, client RedisEnqueuer) (Dispatcher, error) {
	switch cfg.Driver {
	case DriverSync:
		return NewSyncDispatcher(), nil
	case DriverWorkerpool:
		return NewWorkerpoolDispatcher(cfg.Workerpool), nil
	case DriverRedis:
		return NewRedisDispatcher(client), nil
	case DriverDatabase:
		return NewDatabaseDispatcher(cfg.Database)
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}
