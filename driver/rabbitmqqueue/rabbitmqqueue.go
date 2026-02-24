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
func New(url string) (*queue.Queue, error) {
	return NewWithConfig(Config{URL: url})
}

// NewWithConfig creates a high-level Queue using an explicit RabbitMQ driver config.
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
