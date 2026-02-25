package sqsqueue

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
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
// @group Constructors
//
// Example: sqs shorthand constructor
//
//	q, err := sqsqueue.New(
//		"us-east-1",
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func New(region string, opts ...queue.Option) (*queue.Queue, error) {
	return NewWithConfig(Config{Region: region}, opts...)
}

// NewWithConfig creates a high-level Queue using an explicit SQS driver config.
// @group Constructors
//
// Example: sqs config constructor
//
//	q, err := sqsqueue.NewWithConfig(
//		sqsqueue.Config{
//			DriverBaseConfig: queueconfig.DriverBaseConfig{
//				DefaultQueue: "critical", // default if empty: "default"
//				Observer:     nil,        // default: nil
//			},
//			Region: "us-east-1", // default if empty: "us-east-1"
//			Endpoint: "",        // optional; set for LocalStack/custom endpoint
//			AccessKey: "",       // optional; static credentials
//			SecretKey: "",       // optional; static credentials
//		},
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	cfg = normalizeConfig(cfg)
	rootCfg := queue.Config{
		Driver:       queue.DriverSQS,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return driverbridge.NewQueueFromDriver(rootCfg, newSQSQueue(cfg), func(workers int) (any, error) {
		return newSQSWorker(sqsWorkerConfig{
			DefaultQueue: cfg.DefaultQueue,
			SQSRegion:    cfg.Region,
			SQSEndpoint:  cfg.Endpoint,
			SQSAccessKey: cfg.AccessKey,
			SQSSecretKey: cfg.SecretKey,
			Workers:      workers,
			Observer:     cfg.Observer,
		}), nil
	}, opts...)
}

func normalizeConfig(cfg Config) Config {
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return cfg
}
