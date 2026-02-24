//go:build integration

package all_test

import (
	"fmt"

	. "github.com/goforj/queue"
	"github.com/goforj/queue/driver/mysqlqueue"
	"github.com/goforj/queue/driver/natsqueue"
	"github.com/goforj/queue/driver/postgresqueue"
	"github.com/goforj/queue/driver/rabbitmqqueue"
	"github.com/goforj/queue/driver/redisqueue"
	"github.com/goforj/queue/driver/sqlitequeue"
	"github.com/goforj/queue/driver/sqsqueue"
)

func newQueueRuntime(cfg Config) (QueueRuntime, error) {
	switch cfg.Driver {
	case DriverNull, DriverSync, DriverWorkerpool:
		return NewQueue(cfg)
	case DriverRedis:
		return redisqueue.NewQueue(redisqueue.Config{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case DriverNATS:
		return natsqueue.NewQueue(natsqueue.Config{URL: cfg.NATSURL, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case DriverSQS:
		return sqsqueue.NewQueue(sqsqueue.Config{Region: cfg.SQSRegion, Endpoint: cfg.SQSEndpoint, AccessKey: cfg.SQSAccessKey, SecretKey: cfg.SQSSecretKey, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case DriverRabbitMQ:
		return rabbitmqqueue.NewQueue(rabbitmqqueue.Config{URL: cfg.RabbitMQURL, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case DriverDatabase:
		switch cfg.DatabaseDriver {
		case "mysql":
			return mysqlqueue.NewQueue(mysqlqueue.Config{DB: cfg.Database, DSN: cfg.DatabaseDSN, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer, ProcessingRecoveryGrace: cfg.DatabaseProcessingRecoveryGrace, ProcessingLeaseNoTimeout: cfg.DatabaseProcessingLeaseNoTimeout})
		case "pgx", "postgres":
			return postgresqueue.NewQueue(postgresqueue.Config{DB: cfg.Database, DSN: cfg.DatabaseDSN, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer, ProcessingRecoveryGrace: cfg.DatabaseProcessingRecoveryGrace, ProcessingLeaseNoTimeout: cfg.DatabaseProcessingLeaseNoTimeout})
		case "sqlite":
			return sqlitequeue.NewQueue(sqlitequeue.Config{DB: cfg.Database, DSN: cfg.DatabaseDSN, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer, ProcessingRecoveryGrace: cfg.DatabaseProcessingRecoveryGrace, ProcessingLeaseNoTimeout: cfg.DatabaseProcessingLeaseNoTimeout})
		default:
			return nil, fmt.Errorf("unsupported database driver %q", cfg.DatabaseDriver)
		}
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}

func newQueue(cfg Config, opts ...Option) (*Queue, error) {
	switch cfg.Driver {
	case DriverNull, DriverSync, DriverWorkerpool:
		return New(cfg, opts...)
	case DriverRedis:
		return redisqueue.NewWithConfig(redisqueue.Config{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer}, opts...)
	case DriverNATS:
		return natsqueue.NewWithConfig(natsqueue.Config{URL: cfg.NATSURL, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer}, opts...)
	case DriverSQS:
		return sqsqueue.NewWithConfig(sqsqueue.Config{Region: cfg.SQSRegion, Endpoint: cfg.SQSEndpoint, AccessKey: cfg.SQSAccessKey, SecretKey: cfg.SQSSecretKey, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer}, opts...)
	case DriverRabbitMQ:
		return rabbitmqqueue.NewWithConfig(rabbitmqqueue.Config{URL: cfg.RabbitMQURL, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer}, opts...)
	case DriverDatabase:
		switch cfg.DatabaseDriver {
		case "mysql":
			return mysqlqueue.NewWithConfig(mysqlqueue.Config{DB: cfg.Database, DSN: cfg.DatabaseDSN, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer, ProcessingRecoveryGrace: cfg.DatabaseProcessingRecoveryGrace, ProcessingLeaseNoTimeout: cfg.DatabaseProcessingLeaseNoTimeout}, opts...)
		case "pgx", "postgres":
			return postgresqueue.NewWithConfig(postgresqueue.Config{DB: cfg.Database, DSN: cfg.DatabaseDSN, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer, ProcessingRecoveryGrace: cfg.DatabaseProcessingRecoveryGrace, ProcessingLeaseNoTimeout: cfg.DatabaseProcessingLeaseNoTimeout}, opts...)
		case "sqlite":
			return sqlitequeue.NewWithConfig(sqlitequeue.Config{DB: cfg.Database, DSN: cfg.DatabaseDSN, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer, ProcessingRecoveryGrace: cfg.DatabaseProcessingRecoveryGrace, ProcessingLeaseNoTimeout: cfg.DatabaseProcessingLeaseNoTimeout}, opts...)
		default:
			return nil, fmt.Errorf("unsupported database driver %q", cfg.DatabaseDriver)
		}
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}
