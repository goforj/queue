package sqsqueue

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queueconfig"
)

// Config configures the SQS driver module constructor.
type Config struct {
	queueconfig.DriverBaseConfig
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

// New creates a high-level Queue using the SQS backend.
func New(region string) (*queue.Queue, error) {
	return NewWithConfig(Config{Region: region})
}

// NewWithConfig creates a high-level Queue using an explicit SQS driver config.
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewRuntime(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

// NewRuntime creates a low-level QueueRuntime using the SQS backend.
func NewRuntime(cfg Config) (queue.QueueRuntime, error) {
	cfg = normalizeConfig(cfg)
	rootCfg := queue.Config{
		Driver:       queue.DriverSQS,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return queue.NewQueueFromDriver(rootCfg, newSQSQueue(cfg), func(workers int) (queue.DriverWorkerBackend, error) {
		return newSQSWorker(sqsWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			SQSRegion:    cfg.Region,
			SQSEndpoint:  cfg.Endpoint,
			SQSAccessKey: cfg.AccessKey,
			SQSSecretKey: cfg.SecretKey,
			Workers:      workers,
			Observer:     cfg.Observer,
		}), nil
	})
}

func normalizeConfig(cfg Config) Config {
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return cfg
}
