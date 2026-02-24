package redisqueue

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
	"github.com/goforj/queue/queueconfig"
	"github.com/hibiken/asynq"
)

// Config configures the Redis/Asynq driver module constructor.
type Config struct {
	queueconfig.DriverBaseConfig
	Addr     string
	Password string
	DB       int
}

// New creates a high-level Queue using the Redis backend.
func New(addr string) (*queue.Queue, error) {
	return NewWithConfig(Config{Addr: addr})
}

// NewWithConfig creates a high-level Queue using an explicit Redis driver config.
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverRedis,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	backend := newRedisQueue(newAsynqClient(cfg), newAsynqInspector(cfg), true)
	q, err := driverbridge.NewQueueFromDriver(rootCfg, backend, func(workers int) (any, error) {
		return newRedisWorker(
			asynq.NewServer(asynq.RedisClientOpt{
				Addr:     cfg.Addr,
				Password: cfg.Password,
				DB:       cfg.DB,
			}, asynq.Config{Concurrency: workers}),
			asynq.NewServeMux(),
		), nil
	}, opts...)
	if err != nil {
		return nil, err
	}
	return q, nil
}
