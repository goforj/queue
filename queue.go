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
	// Start initializes background resources for drivers that need them.
	Start(ctx context.Context) error

	// Enqueue submits a task for execution.
	Enqueue(ctx context.Context, task Task) error

	// Register associates a handler with a task type.
	Register(taskType string, handler Handler)

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

// Config configures queue runtime creation for New.
// @group Config
type Config struct {
	Driver Driver

	DefaultQueue string

	Database       *sql.DB
	DatabaseDriver string
	DatabaseDSN    string

	RedisAddr     string
	RedisPassword string
	RedisDB       int

	NATSURL string
}

func newSyncQueue() Queue {
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
//	_ = q.Enqueue(
//		context.Background(),
//		queue.NewTask("emails:send").
//			Payload(EmailPayload{ID: 1}).
//			OnQueue("default"),
//	)
func New(cfg Config) (Queue, error) {
	switch cfg.Driver {
	case DriverSync:
		return newSyncQueue(), nil
	case DriverWorkerpool:
		return newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{}), nil
	case DriverRedis:
		if cfg.RedisAddr == "" {
			return nil, fmt.Errorf("redis addr is required")
		}
		return newRedisQueue(newAsynqClient(cfg), true), nil
	case DriverDatabase:
		return newDatabaseQueue(cfg.databaseConfig())
	case DriverNATS:
		if cfg.NATSURL == "" {
			return nil, fmt.Errorf("nats url is required")
		}
		return newNATSQueue(cfg.NATSURL), nil
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}
