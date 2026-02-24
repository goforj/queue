package rabbitmqqueue

import (
	"fmt"

	"github.com/goforj/queue"
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
	raw, err := NewRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

// NewRuntime creates a low-level QueueRuntime using the RabbitMQ backend.
func NewRuntime(cfg Config) (queue.QueueRuntime, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("rabbitmq url is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverRabbitMQ,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return queue.NewQueueFromDriver(rootCfg, newRabbitMQQueue(cfg.URL, cfg.DefaultQueue), func(workers int) (queue.DriverWorkerBackend, error) {
		return newRabbitMQWorker(rabbitMQWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			RabbitMQURL:  cfg.URL,
			Workers:      workers,
			Observer:     cfg.Observer,
		}), nil
	})
}
