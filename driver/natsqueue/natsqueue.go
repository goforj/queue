package natsqueue

import (
	"fmt"

	"github.com/goforj/queue"
)

// Config configures the NATS driver module constructor.
type Config struct {
	URL          string
	DefaultQueue string
	Observer     queue.Observer
}

// New creates a high-level Queue using the NATS backend.
func New(url string) (*queue.Queue, error) {
	return NewWithConfig(Config{URL: url})
}

// NewWithConfig creates a high-level Queue using an explicit NATS driver config.
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewQueue(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

// NewQueue creates a low-level QueueRuntime using the NATS backend.
func NewQueue(cfg Config) (queue.QueueRuntime, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("nats url is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverNATS,
		NATSURL:      cfg.URL,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return queue.NewQueueFromDriver(rootCfg, newNATSQueue(rootCfg.NATSURL), func(c queue.Config, workers int) (queue.DriverWorkerBackend, error) {
		return newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      c.NATSURL,
			Workers:  workers,
			Observer: c.Observer,
		}), nil
	})
}
