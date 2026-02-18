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
	Workers       int
	QueueCapacity int
	TaskTimeout   time.Duration
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

// Config configures dispatcher creation for NewDispatcher.
// @group Config
type Config struct {
	Driver Driver

	Workers       int
	QueueCapacity int
	TaskTimeout   time.Duration

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

func newSyncDispatcher() Dispatcher {
	return newLocalDispatcherWithConfig(DriverSync, WorkerpoolConfig{})
}

func (cfg Config) workerpoolConfig() WorkerpoolConfig {
	return WorkerpoolConfig{
		Workers:       cfg.Workers,
		QueueCapacity: cfg.QueueCapacity,
		TaskTimeout:   cfg.TaskTimeout,
	}
}

func (cfg Config) databaseConfig() DatabaseConfig {
	autoMigrate := cfg.AutoMigrate
	if !autoMigrate {
		autoMigrate = true
	}
	return DatabaseConfig{
		DB:           cfg.Database,
		DriverName:   cfg.DatabaseDriver,
		DSN:          cfg.DatabaseDSN,
		Workers:      cfg.Workers,
		PollInterval: cfg.PollInterval,
		DefaultQueue: cfg.DefaultQueue,
		AutoMigrate:  autoMigrate,
	}
}

// NewDispatcher creates a dispatcher based on Config.Driver.
// @group Constructors
//
// Example: new dispatcher from config
//
//	dispatcher, err := queue.NewDispatcher(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		return nil
//	})
//	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
func NewDispatcher(cfg Config) (Dispatcher, error) {
	switch cfg.Driver {
	case DriverSync:
		return newSyncDispatcher(), nil
	case DriverWorkerpool:
		return newLocalDispatcherWithConfig(DriverWorkerpool, cfg.workerpoolConfig().normalize()), nil
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
