//go:build integration

package queue

import (
	"fmt"
	"testing"
	"time"
)

func TestDispatcherContract_Redis(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}

	factory := contractFactory{
		name: "redis",
		newDispatcher: func(t *testing.T) Dispatcher {
			dispatcher, err := NewDispatcher(Config{
				Driver:    DriverRedis,
				RedisAddr: integrationRedis.addr,
			})
			if err != nil {
				t.Fatalf("new redis dispatcher failed: %v", err)
			}
			return dispatcher
		},
		requiresRegisteredHandle: false,
		assertMissingHandlerErr:  false,
		uniqueTTL:                time.Second,
		uniqueExpiryWait:         1200 * time.Millisecond,
	}
	runDispatcherContractSuite(t, factory)
}

func TestDispatcherContract_DatabaseMySQL(t *testing.T) {
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
		newDispatcher: func(_ *testing.T) Dispatcher {
			dispatcher, err := NewDispatcher(Config{
				Driver:         DriverDatabase,
				DatabaseDriver: cfg.DriverName,
				DatabaseDSN:    cfg.DSN,
				Workers:        cfg.Workers,
				PollInterval:   cfg.PollInterval,
			})
			if err != nil {
				t.Fatalf("new mysql dispatcher failed: %v", err)
			}
			return dispatcher
		},
		requiresRegisteredHandle: true,
		assertMissingHandlerErr:  true,
		beforeEach: func(t *testing.T) {
			resetQueueTables(t, cfg)
		},
	}
	runDispatcherContractSuite(t, factory)
}

func TestDispatcherContract_DatabasePostgres(t *testing.T) {
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
		newDispatcher: func(_ *testing.T) Dispatcher {
			dispatcher, err := NewDispatcher(Config{
				Driver:         DriverDatabase,
				DatabaseDriver: cfg.DriverName,
				DatabaseDSN:    cfg.DSN,
				Workers:        cfg.Workers,
				PollInterval:   cfg.PollInterval,
			})
			if err != nil {
				t.Fatalf("new postgres dispatcher failed: %v", err)
			}
			return dispatcher
		},
		requiresRegisteredHandle: true,
		assertMissingHandlerErr:  true,
		beforeEach: func(t *testing.T) {
			resetQueueTables(t, cfg)
		},
	}
	runDispatcherContractSuite(t, factory)
}

func TestDispatcherContract_DatabaseSQLiteIntegration(t *testing.T) {
	if !integrationBackendEnabled("sqlite") {
		t.Skip("sqlite integration backend not selected")
	}
	factory := contractFactory{
		name: "database-sqlite",
		newDispatcher: func(t *testing.T) Dispatcher {
			cfg := DatabaseConfig{
				DriverName:   "sqlite",
				DSN:          fmt.Sprintf("%s/contract-integration-%d.db", t.TempDir(), time.Now().UnixNano()),
				Workers:      1,
				PollInterval: 10 * time.Millisecond,
			}
			dispatcher, err := NewDispatcher(Config{
				Driver:         DriverDatabase,
				DatabaseDriver: cfg.DriverName,
				DatabaseDSN:    cfg.DSN,
				Workers:        cfg.Workers,
				PollInterval:   cfg.PollInterval,
			})
			if err != nil {
				t.Fatalf("new sqlite dispatcher failed: %v", err)
			}
			return dispatcher
		},
		requiresRegisteredHandle: true,
		assertMissingHandlerErr:  true,
	}
	runDispatcherContractSuite(t, factory)
}
