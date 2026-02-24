package natsqueue

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/queueconfig"
)

// Config configures the NATS driver module constructor.
type Config struct {
	queueconfig.DriverBaseConfig
	URL string
}

// New creates a high-level Queue using the NATS backend.
func New(url string) (*queue.Queue, error) {
	return NewWithConfig(Config{URL: url})
}

// NewWithConfig creates a high-level Queue using an explicit NATS driver config.
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

// NewRuntime creates a low-level QueueRuntime using the NATS backend.
func NewRuntime(cfg Config) (queue.QueueRuntime, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("nats url is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverNATS,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return queue.NewQueueFromDriver(rootCfg, newNATSQueue(cfg.URL), func(workers int) (queue.DriverWorkerBackend, error) {
		return newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      cfg.URL,
			Workers:  workers,
			Observer: cfg.Observer,
		}), nil
	})
}
