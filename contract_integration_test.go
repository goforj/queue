//go:build integration

package queue

import (
	"fmt"
	"testing"
	"time"
)

func TestQueueContract_Redis(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}

	factory := contractFactory{
		name: "redis",
		newQueue: func(t *testing.T) Queue {
			q, err := New(Config{
				Driver:    DriverRedis,
				RedisAddr: integrationRedis.addr,
			})
			if err != nil {
				t.Fatalf("new redis queue failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		backoffUnsupported:       true,
		uniqueTTL:                time.Second,
		uniqueExpiryWait:         1200 * time.Millisecond,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_DatabaseMySQL(t *testing.T) {
	if !integrationBackendEnabled("mysql") {
		t.Skip("mysql integration backend not selected")
	}
	cfg := DatabaseConfig{
		DriverName:   "mysql",
		DSN:          fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	factory := contractFactory{
		name: "database-mysql",
		newQueue: func(_ *testing.T) Queue {
			q, err := New(Config{
				Driver:         DriverDatabase,
				DatabaseDriver: cfg.DriverName,
				DatabaseDSN:    cfg.DSN,
			})
			if err != nil {
				t.Fatalf("new mysql q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: true,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		beforeEach: func(t *testing.T) {
			resetQueueTables(t, cfg)
		},
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_DatabasePostgres(t *testing.T) {
	if !integrationBackendEnabled("postgres") {
		t.Skip("postgres integration backend not selected")
	}
	cfg := DatabaseConfig{
		DriverName:   "pgx",
		DSN:          fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	factory := contractFactory{
		name: "database-postgres",
		newQueue: func(_ *testing.T) Queue {
			q, err := New(Config{
				Driver:         DriverDatabase,
				DatabaseDriver: cfg.DriverName,
				DatabaseDSN:    cfg.DSN,
			})
			if err != nil {
				t.Fatalf("new postgres q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: true,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		beforeEach: func(t *testing.T) {
			resetQueueTables(t, cfg)
		},
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_DatabaseSQLiteIntegration(t *testing.T) {
	if !integrationBackendEnabled("sqlite") {
		t.Skip("sqlite integration backend not selected")
	}
	factory := contractFactory{
		name: "database-sqlite",
		newQueue: func(t *testing.T) Queue {
			cfg := DatabaseConfig{
				DriverName:   "sqlite",
				DSN:          fmt.Sprintf("%s/contract-integration-%d.db", t.TempDir(), time.Now().UnixNano()),
				Workers:      1,
				PollInterval: 10 * time.Millisecond,
			}
			q, err := New(Config{
				Driver:         DriverDatabase,
				DatabaseDriver: cfg.DriverName,
				DatabaseDSN:    cfg.DSN,
			})
			if err != nil {
				t.Fatalf("new sqlite q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: true,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_NATS(t *testing.T) {
	if !integrationBackendEnabled("nats") {
		t.Skip("nats integration backend not selected")
	}
	factory := contractFactory{
		name: "nats",
		newQueue: func(_ *testing.T) Queue {
			q, err := New(Config{
				Driver:  DriverNATS,
				NATSURL: integrationNATS.url,
			})
			if err != nil {
				t.Fatalf("new nats q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_SQS(t *testing.T) {
	if !integrationBackendEnabled("sqs") {
		t.Skip("sqs integration backend not selected")
	}
	factory := contractFactory{
		name: "sqs",
		newQueue: func(_ *testing.T) Queue {
			q, err := New(Config{
				Driver:       DriverSQS,
				SQSEndpoint:  integrationSQS.endpoint,
				SQSRegion:    integrationSQS.region,
				SQSAccessKey: integrationSQS.accessKey,
				SQSSecretKey: integrationSQS.secretKey,
			})
			if err != nil {
				t.Fatalf("new sqs q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_RabbitMQ(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	factory := contractFactory{
		name: "rabbitmq",
		newQueue: func(_ *testing.T) Queue {
			q, err := New(Config{
				Driver:      DriverRabbitMQ,
				RabbitMQURL: integrationRabbitMQ.url,
			})
			if err != nil {
				t.Fatalf("new rabbitmq q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
	}
	runQueueContractSuite(t, factory)
}
