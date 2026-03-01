//go:build integration

package root_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

func TestQueueContract_Redis(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}
	ensureRedis(t)

	factory := contractFactory{
		name:           testenv.BackendRedis,
		expectedDriver: queue.DriverRedis,
		newQueue: func(t *testing.T) QueueRuntime {
			q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
			if err != nil {
				t.Fatalf("new redis queue failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		backoffUnsupported:       true,
		supportsPause:            true,
		supportsNativeStats:      true,
		uniqueTTL:                time.Second,
		uniqueExpiryWait:         1200 * time.Millisecond,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_DatabaseMySQL(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendMySQL) {
		t.Skip("mysql integration backend not selected")
	}
	ensureMySQLDB(t)
	cfg := queue.DatabaseConfig{
		DriverName:   testenv.BackendMySQL,
		DSN:          mysqlDSN(integrationMySQL.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	factory := contractFactory{
		name:           "database-mysql",
		expectedDriver: queue.DriverDatabase,
		newQueue: func(_ *testing.T) QueueRuntime {
			q, err := newQueueRuntime(mysqlCfg(cfg.DSN))
			if err != nil {
				t.Fatalf("new mysql q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: true,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		supportsPause:            false,
		supportsNativeStats:      true,
		beforeEach: func(t *testing.T) {
			resetQueueTables(t, cfg)
		},
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_DatabasePostgres(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendPostgres) {
		t.Skip("postgres integration backend not selected")
	}
	ensurePostgresDB(t)
	cfg := queue.DatabaseConfig{
		DriverName:   "pgx",
		DSN:          postgresDSN(integrationPostgres.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	factory := contractFactory{
		name:           "database-postgres",
		expectedDriver: queue.DriverDatabase,
		newQueue: func(_ *testing.T) QueueRuntime {
			q, err := newQueueRuntime(postgresCfg(cfg.DSN))
			if err != nil {
				t.Fatalf("new postgres q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: true,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		supportsPause:            false,
		supportsNativeStats:      true,
		beforeEach: func(t *testing.T) {
			resetQueueTables(t, cfg)
		},
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_DatabaseSQLiteIntegration(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendSQLite) {
		t.Skip("sqlite integration backend not selected")
	}
	factory := contractFactory{
		name:           "database-sqlite",
		expectedDriver: queue.DriverDatabase,
		newQueue: func(t *testing.T) QueueRuntime {
			cfg := queue.DatabaseConfig{
				DriverName:   testenv.BackendSQLite,
				DSN:          fmt.Sprintf("%s/contract-integration-%d.db", t.TempDir(), time.Now().UnixNano()),
				Workers:      1,
				PollInterval: 10 * time.Millisecond,
			}
			q, err := newQueueRuntime(sqliteCfg(cfg.DSN))
			if err != nil {
				t.Fatalf("new sqlite q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: true,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		supportsPause:            false,
		supportsNativeStats:      true,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_NATS(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendNATS) {
		t.Skip("nats integration backend not selected")
	}
	ensureNATS(t)
	factory := contractFactory{
		name:           testenv.BackendNATS,
		expectedDriver: queue.DriverNATS,
		newQueue: func(_ *testing.T) QueueRuntime {
			q, err := newQueueRuntime(natsCfg(integrationNATS.url))
			if err != nil {
				t.Fatalf("new nats q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		supportsPause:            false,
		supportsNativeStats:      false,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_SQS(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendSQS) {
		t.Skip("sqs integration backend not selected")
	}
	ensureSQS(t)
	factory := contractFactory{
		name:           testenv.BackendSQS,
		expectedDriver: queue.DriverSQS,
		newQueue: func(_ *testing.T) QueueRuntime {
			q, err := newQueueRuntime(sqsCfg(
				integrationSQS.region,
				integrationSQS.endpoint,
				integrationSQS.accessKey,
				integrationSQS.secretKey,
			))
			if err != nil {
				t.Fatalf("new sqs q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		supportsPause:            false,
		supportsNativeStats:      false,
	}
	runQueueContractSuite(t, factory)
}

func TestQueueContract_RabbitMQ(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRabbitMQ) {
		t.Skip("rabbitmq integration backend not selected")
	}
	ensureRabbitMQ(t)
	factory := contractFactory{
		name:           testenv.BackendRabbitMQ,
		expectedDriver: queue.DriverRabbitMQ,
		newQueue: func(_ *testing.T) QueueRuntime {
			q, err := newQueueRuntime(rabbitmqCfg(integrationRabbitMQ.url))
			if err != nil {
				t.Fatalf("new rabbitmq q failed: %v", err)
			}
			return q
		},
		requiresRegisteredHandle: false,
		requiresQueueName:        true,
		assertMissingHandlerErr:  false,
		supportsPause:            false,
		supportsNativeStats:      false,
	}
	runQueueContractSuite(t, factory)
}
