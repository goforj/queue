package rabbitmqqueue

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
	"github.com/goforj/queue/queueconfig"
)

// Config configures the RabbitMQ driver module constructor.
type Config struct {
	queueconfig.DriverBaseConfig
	URL string
}

// New creates a high-level Queue using the RabbitMQ backend.
// @group Constructors
//
// Example: rabbitmq shorthand constructor
//
//	q, err := rabbitmqqueue.New(
//		"amqp://guest:guest@127.0.0.1:5672/",
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func New(url string, opts ...queue.Option) (*queue.Queue, error) {
	return NewWithConfig(Config{URL: url}, opts...)
}

// NewWithConfig creates a high-level Queue using an explicit RabbitMQ driver config.
// @group Constructors
//
// Example: rabbitmq config constructor
//
//	q, err := rabbitmqqueue.NewWithConfig(
//		rabbitmqqueue.Config{
//			DriverBaseConfig: queueconfig.DriverBaseConfig{
//				DefaultQueue: "critical", // default if empty: "default"
//				Observer:     nil,        // default: nil
//			},
//			URL: "amqp://guest:guest@127.0.0.1:5672/", // required
//		},
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("rabbitmq url is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverRabbitMQ,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return driverbridge.NewQueueFromDriver(rootCfg, newRabbitMQQueue(cfg.URL, cfg.DefaultQueue), func(workers int) (any, error) {
		return newRabbitMQWorker(rabbitMQWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			RabbitMQURL:  cfg.URL,
			Workers:      workers,
			Observer:     cfg.Observer,
		}), nil
	}, opts...)
}
