package sqsqueue

import "github.com/goforj/queue"

// Config configures the SQS driver module constructor.
type Config struct {
	Region       string
	Endpoint     string
	AccessKey    string
	SecretKey    string
	DefaultQueue string
	Observer     queue.Observer
}

// New creates a high-level Queue using the SQS backend.
func New(region string) (*queue.Queue, error) {
	return NewWithConfig(Config{Region: region})
}

// NewWithConfig creates a high-level Queue using an explicit SQS driver config.
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewQueue(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

// NewQueue creates a low-level QueueRuntime using the SQS backend.
func NewQueue(cfg Config) (queue.QueueRuntime, error) {
	rootCfg := queue.Config{
		Driver:       queue.DriverSQS,
		SQSRegion:    cfg.Region,
		SQSEndpoint:  cfg.Endpoint,
		SQSAccessKey: cfg.AccessKey,
		SQSSecretKey: cfg.SecretKey,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return queue.NewQueueFromDriver(rootCfg, newSQSQueue(rootCfg), func(c queue.Config, workers int) (queue.DriverWorkerBackend, error) {
		return newSQSWorker(sqsWorkerConfig{
			DefaultQueue: c.DefaultQueue,
			SQSRegion:    c.SQSRegion,
			SQSEndpoint:  c.SQSEndpoint,
			SQSAccessKey: c.SQSAccessKey,
			SQSSecretKey: c.SQSSecretKey,
			Workers:      workers,
			Observer:     c.Observer,
		}), nil
	})
}
