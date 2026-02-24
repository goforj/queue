package redisqueue

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/hibiken/asynq"
)

// Config configures the Redis/Asynq driver module constructor.
type Config struct {
	Addr         string
	Password     string
	DB           int
	DefaultQueue string
	Observer     queue.Observer
}

// New creates a high-level Queue using the Redis backend.
func New(addr string) (*queue.Queue, error) {
	return NewWithConfig(Config{Addr: addr})
}

// NewWithConfig creates a high-level Queue using an explicit Redis driver config.
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewQueue(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

// NewQueue creates a low-level QueueRuntime using the Redis backend.
func NewQueue(cfg Config) (queue.QueueRuntime, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is required")
	}
	rootCfg := queue.Config{
		Driver:        queue.DriverRedis,
		RedisAddr:     cfg.Addr,
		RedisPassword: cfg.Password,
		RedisDB:       cfg.DB,
		DefaultQueue:  cfg.DefaultQueue,
		Observer:      cfg.Observer,
	}
	backend := newRedisQueue(newAsynqClient(rootCfg), newAsynqInspector(rootCfg), true)
	return queue.NewQueueFromDriver(rootCfg, backend, func(c queue.Config, workers int) (queue.DriverWorkerBackend, error) {
		return newRedisWorker(
			asynq.NewServer(asynq.RedisClientOpt{
				Addr:     c.RedisAddr,
				Password: c.RedisPassword,
				DB:       c.RedisDB,
			}, asynq.Config{Concurrency: workers}),
			asynq.NewServeMux(),
		), nil
	})
}
