//go:build integration

package root_test

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/mysqlqueue"
	"github.com/goforj/queue/driver/natsqueue"
	"github.com/goforj/queue/driver/postgresqueue"
	"github.com/goforj/queue/driver/rabbitmqqueue"
	"github.com/goforj/queue/driver/redisqueue"
	"github.com/goforj/queue/driver/sqlitequeue"
	"github.com/goforj/queue/driver/sqsqueue"
)

func newQueueRuntime(cfg queue.Config) (queue.QueueRuntime, error) {
	switch cfg.Driver {
	case queue.DriverNull, queue.DriverSync, queue.DriverWorkerpool:
		return newQueueRuntime(cfg)
	case queue.DriverRedis:
		return redisqueue.NewQueue(redisqueue.Config{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case queue.DriverNATS:
		return natsqueue.NewQueue(natsqueue.Config{URL: cfg.NATSURL, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case queue.DriverSQS:
		return sqsqueue.NewQueue(sqsqueue.Config{Region: cfg.SQSRegion, Endpoint: cfg.SQSEndpoint, AccessKey: cfg.SQSAccessKey, SecretKey: cfg.SQSSecretKey, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case queue.DriverRabbitMQ:
		return rabbitmqqueue.NewQueue(rabbitmqqueue.Config{URL: cfg.RabbitMQURL, DefaultQueue: cfg.DefaultQueue, Observer: cfg.Observer})
	case queue.DriverDatabase:
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
