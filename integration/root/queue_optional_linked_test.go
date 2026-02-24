//go:build integration

package root_test

import (
	"context"
	"strings"
	"testing"

	"github.com/goforj/queue"
)

func testQueueDriver(q QueueRuntime) queue.Driver {
	if driverAware, ok := q.(interface{ Driver() queue.Driver }); ok {
		return driverAware.Driver()
	}
	return queue.Driver("")
}

func TestDriverModuleNewQueues(t *testing.T) {
	t.Run("redis", func(t *testing.T) {
		q, err := newQueueRuntime(redisCfg("127.0.0.1:6379"))
		if err != nil {
			t.Fatalf("new q failed: %v", err)
		}
		if testQueueDriver(q) != queue.DriverRedis {
			t.Fatalf("expected redis driver, got %q", testQueueDriver(q))
		}
	})
	t.Run("nats", func(t *testing.T) {
		q, err := newQueueRuntime(natsCfg("nats://127.0.0.1:4222"))
		if err != nil {
			t.Fatalf("new q failed: %v", err)
		}
		if testQueueDriver(q) != queue.DriverNATS {
			t.Fatalf("expected nats driver, got %q", testQueueDriver(q))
		}
	})
	t.Run("sqs", func(t *testing.T) {
		q, err := newQueueRuntime(sqsCfg("us-east-1", "", "", ""))
		if err != nil {
			t.Fatalf("new q failed: %v", err)
		}
		if testQueueDriver(q) != queue.DriverSQS {
			t.Fatalf("expected sqs driver, got %q", testQueueDriver(q))
		}
	})
	t.Run("rabbitmq", func(t *testing.T) {
		q, err := newQueueRuntime(rabbitmqCfg("amqp://guest:guest@127.0.0.1:5672/"))
		if err != nil {
			t.Fatalf("new q failed: %v", err)
		}
		if testQueueDriver(q) != queue.DriverRabbitMQ {
			t.Fatalf("expected rabbitmq driver, got %q", testQueueDriver(q))
		}
	})
	t.Run("database", func(t *testing.T) {
		q, err := newQueueRuntime(sqliteCfg(t.TempDir() + "/queue.db"))
		if err != nil {
			t.Fatalf("new q failed: %v", err)
		}
		if testQueueDriver(q) != queue.DriverDatabase {
			t.Fatalf("expected database driver, got %q", testQueueDriver(q))
		}
	})
}

func TestDriverModuleQueueSelectionByConfig(t *testing.T) {
	testCases := []struct {
		name   string
		cfg    any
		driver queue.Driver
	}{
		{
			name:   "database",
			cfg:    sqliteCfg(t.TempDir() + "/queue.db"),
			driver: queue.DriverDatabase,
		},
		{name: "redis", cfg: redisCfg("127.0.0.1:6379"), driver: queue.DriverRedis},
		{name: "nats", cfg: natsCfg("nats://127.0.0.1:4222"), driver: queue.DriverNATS},
		{name: "sqs", cfg: sqsCfg("us-east-1", "", "", ""), driver: queue.DriverSQS},
		{name: "rabbitmq", cfg: rabbitmqCfg("amqp://guest:guest@127.0.0.1:5672/"), driver: queue.DriverRabbitMQ},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := newQueueRuntime(tc.cfg)
			if err != nil {
				t.Fatalf("new q failed: %v", err)
			}
			if testQueueDriver(q) != tc.driver {
				t.Fatalf("expected %q driver, got %q", tc.driver, testQueueDriver(q))
			}
		})
	}
}

func TestDriverModuleValidationErrors(t *testing.T) {
	t.Run("redis_missing_addr", func(t *testing.T) {
		q, err := newQueueRuntime(redisCfg(""))
		if err == nil {
			t.Fatal("expected constructor error for missing redis addr")
		}
		if q != nil {
			t.Fatal("expected nil q")
		}
		if !strings.Contains(err.Error(), "redis addr is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("nats_missing_url", func(t *testing.T) {
		q, err := newQueueRuntime(natsCfg(""))
		if err == nil {
			t.Fatal("expected constructor error for missing nats url")
		}
		if q != nil {
			t.Fatal("expected nil q")
		}
		if !strings.Contains(err.Error(), "nats url is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("rabbitmq_missing_url", func(t *testing.T) {
		q, err := newQueueRuntime(rabbitmqCfg(""))
		if err == nil {
			t.Fatal("expected constructor error for missing rabbitmq url")
		}
		if q != nil {
			t.Fatal("expected nil q")
		}
		if !strings.Contains(err.Error(), "rabbitmq url is required") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestQueue_ShutdownNoopForRedis_DriverModule(t *testing.T) {
	redisQueue, err := newQueueRuntime(redisCfg("127.0.0.1:6379"))
	if err != nil {
		t.Fatalf("redis constructor failed: %v", err)
	}
	if err := redisQueue.Shutdown(context.Background()); err != nil {
		t.Fatalf("redis shutdown failed: %v", err)
	}
}

func TestRootNewQueue_RejectsOptionalDrivers(t *testing.T) {
	testCases := []struct {
		name string
		cfg  queue.Config
		want string
	}{
		{name: "redis", cfg: queue.Config{Driver: queue.DriverRedis}, want: "driver moved; use"},
		{name: "nats", cfg: queue.Config{Driver: queue.DriverNATS}, want: "driver moved; use"},
		{name: "sqs", cfg: queue.Config{Driver: queue.DriverSQS}, want: "driver moved; use"},
		{name: "rabbitmq", cfg: queue.Config{Driver: queue.DriverRabbitMQ}, want: "driver moved; use"},
		{name: "database", cfg: queue.Config{Driver: queue.DriverDatabase}, want: "database drivers moved; use"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := newQueueRuntime(tc.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if q != nil {
				t.Fatal("expected nil queue")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
