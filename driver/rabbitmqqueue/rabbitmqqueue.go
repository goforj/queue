package rabbitmqqueue

import (
	"fmt"

	"github.com/goforj/queue"
)

// Config configures the RabbitMQ driver module constructor.
type Config struct {
	URL          string
	DefaultQueue string
	Observer     queue.Observer
}

// New creates a high-level Queue using the RabbitMQ backend.
func New(url string) (*queue.Queue, error) {
	return NewWithConfig(Config{URL: url})
}

// NewWithConfig creates a high-level Queue using an explicit RabbitMQ driver config.
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewQueue(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

// NewQueue creates a low-level QueueRuntime using the RabbitMQ backend.
func NewQueue(cfg Config) (queue.QueueRuntime, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("rabbitmq url is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverRabbitMQ,
		DefaultQueue: cfg.DefaultQueue,
		RabbitMQURL:  cfg.URL,
		Observer:     cfg.Observer,
	}
	return queue.NewQueueFromDriver(rootCfg, newRabbitMQQueue(rootCfg.RabbitMQURL, rootCfg.DefaultQueue), func(c queue.Config, workers int) (queue.DriverWorkerBackend, error) {
		return newRabbitMQWorker(rabbitMQWorkerConfig{
			DefaultQueue: c.DefaultQueue,
			RabbitMQURL:  c.RabbitMQURL,
			Workers:      workers,
			Observer:     c.Observer,
		}), nil
	})
}
