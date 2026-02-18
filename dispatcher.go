package queue

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"time"
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

// DispatcherConfig configures dispatcher creation for NewDispatcher.
// @group Config
type DispatcherConfig struct {
	Driver Driver

	DefaultQueue string

	Database       *sql.DB
	DatabaseDriver string
	DatabaseDSN    string

	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

func newSyncDispatcher() Dispatcher {
	return newLocalDispatcherWithConfig(DriverSync, WorkerpoolConfig{})
}

func (cfg DispatcherConfig) databaseConfig() DatabaseConfig {
	return DatabaseConfig{
		DB:           cfg.Database,
		DriverName:   cfg.DatabaseDriver,
		DSN:          cfg.DatabaseDSN,
		DefaultQueue: cfg.DefaultQueue,
	}
}

// NewDispatcher creates a dispatcher based on DispatcherConfig.Driver.
// @group Constructors
//
// Example: new dispatcher from config
//
//	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
func NewDispatcher(cfg DispatcherConfig) (Dispatcher, error) {
	switch cfg.Driver {
	case DriverSync:
		return newSyncDispatcher(), nil
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
