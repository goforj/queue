package queue

import (
    "context"
    "fmt"
    "runtime"
    "time"

    "github.com/hibiken/asynq"
)

// Dispatcher enqueues tasks and optionally handles local execution.
type Dispatcher interface {
    Driver() Driver
    Start(ctx context.Context) error
    Enqueue(ctx context.Context, task Task, opts ...Option) error
    Register(taskType string, handler Handler)
    Shutdown(ctx context.Context) error
}

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

type Config struct {
    Driver     Driver
    Workerpool WorkerpoolConfig
    Database   DatabaseConfig
}

type RedisEnqueuer interface {
    Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

func NewSyncDispatcher() Dispatcher {
    return newLocalDispatcherWithConfig(DriverSync, WorkerpoolConfig{})
}

func NewWorkerpoolDispatcher(cfg WorkerpoolConfig) Dispatcher {
    return newLocalDispatcherWithConfig(DriverWorkerpool, cfg.normalize())
}

func NewRedisDispatcher(client RedisEnqueuer) Dispatcher {
    return newRedisDispatcher(client)
}

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
