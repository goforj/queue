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
	Addr          string
	Password      string
	DB            int
	AsynqLogger   asynq.Logger
	AsynqLogLevel asynq.LogLevel
}

// New creates a high-level Queue using the Redis backend.
// @group Constructors
//
// Example: redis shorthand constructor
//
//	q, err := redisqueue.New(
//		"127.0.0.1:6379",
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func New(addr string, opts ...queue.Option) (*queue.Queue, error) {
	return NewWithConfig(Config{Addr: addr}, opts...)
}

// NewWithConfig creates a high-level Queue using an explicit Redis driver config.
// @group Constructors
//
// Example: redis config constructor
//
//	q, err := redisqueue.NewWithConfig(
//		redisqueue.Config{
//			DriverBaseConfig: queueconfig.DriverBaseConfig{
//				DefaultQueue: "critical", // default if empty: "default"
//				Observer:     nil,        // default: nil
//			},
//			Addr: "127.0.0.1:6379", // required
//			Password: "",           // optional; default empty
//			DB: 0,                  // optional; default 0
//			AsynqLogger: nil,       // optional; default Asynq logger
//			AsynqLogLevel: 0,       // optional; default Asynq info level
//		},
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
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
			}, asynqServerConfig(cfg, workers)),
			asynq.NewServeMux(),
			cfg.Observer,
		), nil
	}, opts...)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func asynqServerConfig(cfg Config, workers int) asynq.Config {
	serverCfg := asynq.Config{Concurrency: workers}
	if cfg.AsynqLogger != nil {
		serverCfg.Logger = cfg.AsynqLogger
	}
	if cfg.AsynqLogLevel > 0 {
		serverCfg.LogLevel = cfg.AsynqLogLevel
	}
	return serverCfg
}
