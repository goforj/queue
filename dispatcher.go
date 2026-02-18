package queue

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"time"
)

// Queue is the queue abstraction exposed to callers.
type Queue interface {
	// Driver returns the active backend driver.
	Driver() Driver

	// Start initializes background resources for drivers that need them.
	Start(ctx context.Context) error

	// Dispatch submits a task for execution with optional dispatch options.
	Dispatch(taskType string, payload []byte, opts ...Option) error

	// DispatchCtx submits a task with an explicit context for cancellation and deadlines.
	DispatchCtx(ctx context.Context, taskType string, payload []byte, opts ...Option) error

	// Register associates a handler with a task type.
	Register(taskType string, handler Handler)

	// Shutdown drains running work and releases resources.
	Shutdown(ctx context.Context) error
}

// WorkerpoolConfig configures the in-memory workerpool dispatcher.
// @group Config
type WorkerpoolConfig struct {
	Workers            int
	QueueCapacity      int
	DefaultTaskTimeout time.Duration
}

func (c WorkerpoolConfig) normalize() WorkerpoolConfig {
	if c.Workers <= 0 {
		c.Workers = runtime.NumCPU()
	}
	if c.Workers <= 0 {
		c.Workers = 1
	}
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = c.Workers
	}
	return c
}

// QueueConfig configures queuer creation for NewQueue.
// @group Config
type QueueConfig struct {
	Driver Driver

	DefaultQueue string

	Database       *sql.DB
	DatabaseDriver string
	DatabaseDSN    string

	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

func newSyncQueue() Queue {
	return newLocalDispatcherWithConfig(DriverSync, WorkerpoolConfig{})
}

func (cfg QueueConfig) databaseConfig() DatabaseConfig {
	return DatabaseConfig{
		DB:           cfg.Database,
		DriverName:   cfg.DatabaseDriver,
		DSN:          cfg.DatabaseDSN,
		DefaultQueue: cfg.DefaultQueue,
	}
}

// NewQueue creates a queuer based on QueueConfig.Driver.
// @group Constructors
//
// Example: new queuer from config
//
//	queuer, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	queuer.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = queuer.Dispatch("emails:send", []byte(`{"id":1}`))
func NewQueue(cfg QueueConfig) (Queue, error) {
	switch cfg.Driver {
	case DriverSync:
		return newSyncQueue(), nil
	case DriverWorkerpool:
		return newLocalDispatcherWithConfig(DriverWorkerpool, WorkerpoolConfig{}), nil
	case DriverRedis:
		if cfg.RedisAddr == "" {
			return nil, fmt.Errorf("redis addr is required")
		}
		return newRedisDispatcher(newAsynqClient(cfg), true), nil
	case DriverDatabase:
		return newDatabaseDispatcher(cfg.databaseConfig())
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}
