package natsqueue

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
	"github.com/goforj/queue/queueconfig"
)

// Config configures the NATS driver module constructor.
type Config struct {
	queueconfig.DriverBaseConfig
	URL string
}

// New creates a high-level Queue using the NATS backend.
// @group Constructors
//
// Example: nats shorthand constructor
//
//	q, err := natsqueue.New(
//		"nats://127.0.0.1:4222",
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func New(url string, opts ...queue.Option) (*queue.Queue, error) {
	return NewWithConfig(Config{URL: url}, opts...)
}

// NewWithConfig creates a high-level Queue using an explicit NATS driver config.
// @group Constructors
//
// Example: nats config constructor
//
//	q, err := natsqueue.NewWithConfig(
//		natsqueue.Config{
//			DriverBaseConfig: queueconfig.DriverBaseConfig{
//				DefaultQueue: "critical", // default if empty: "default"
//				Observer:     nil,        // default: nil
//			},
//			URL: "nats://127.0.0.1:4222", // required
//		},
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("nats url is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverNATS,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return driverbridge.NewQueueFromDriver(rootCfg, newNATSQueue(cfg.URL), func(workers int) (any, error) {
		return newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      cfg.URL,
			Workers:  workers,
			Observer: cfg.Observer,
		}), nil
	}, opts...)
}
