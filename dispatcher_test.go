package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

type fakeEnqueuer struct{}

func (f fakeEnqueuer) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	return &asynq.TaskInfo{ID: "fake", Type: task.Type()}, nil
}

func (f fakeEnqueuer) Close() error {
	return nil
}

func queueDriver(q Queue) Driver {
	if driverAware, ok := q.(interface{ Driver() Driver }); ok {
		return driverAware.Driver()
	}
	return Driver("")
}

func TestNewSyncDispatcher(t *testing.T) {
	dispatcher, err := NewQueue(QueueConfig{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new dispatcher failed: %v", err)
	}
	if queueDriver(dispatcher) != DriverSync {
		t.Fatalf("expected sync driver, got %q", queueDriver(dispatcher))
	}
}

func TestNewWorkerpoolDispatcher(t *testing.T) {
	dispatcher, err := NewQueue(QueueConfig{
		Driver: DriverWorkerpool,
	})
	if err != nil {
		t.Fatalf("new dispatcher failed: %v", err)
	}
	if queueDriver(dispatcher) != DriverWorkerpool {
		t.Fatalf("expected workerpool driver, got %q", queueDriver(dispatcher))
	}
}

func TestNewRedisDispatcher(t *testing.T) {
	dispatcher, err := NewQueue(QueueConfig{
		Driver:    DriverRedis,
		RedisAddr: "127.0.0.1:6379",
	})
	if err != nil {
		t.Fatalf("new dispatcher failed: %v", err)
	}
	if queueDriver(dispatcher) != DriverRedis {
		t.Fatalf("expected redis driver, got %q", queueDriver(dispatcher))
	}
}

func TestNewDispatcher_SelectsByConfig(t *testing.T) {
	testCases := []struct {
		name   string
		cfg    QueueConfig
		driver Driver
	}{
		{name: "sync", cfg: QueueConfig{Driver: DriverSync}, driver: DriverSync},
		{name: "workerpool", cfg: QueueConfig{Driver: DriverWorkerpool}, driver: DriverWorkerpool},
		{name: "redis", cfg: QueueConfig{Driver: DriverRedis, RedisAddr: "127.0.0.1:6379"}, driver: DriverRedis},
		{
			name: "database",
			cfg: QueueConfig{
				Driver:         DriverDatabase,
				DatabaseDriver: "sqlite",
				DatabaseDSN:    t.TempDir() + "/queue.db",
			},
			driver: DriverDatabase,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher, err := NewQueue(tc.cfg)
			if err != nil {
				t.Fatalf("new dispatcher failed: %v", err)
			}
			if queueDriver(dispatcher) != tc.driver {
				t.Fatalf("expected %q driver, got %q", tc.driver, queueDriver(dispatcher))
			}
		})
	}
}

func TestNewDispatcher_UnknownDriverFails(t *testing.T) {
	dispatcher, err := NewQueue(QueueConfig{Driver: Driver("unknown")})
	if err == nil {
		t.Fatal("expected unknown driver error")
	}
	if dispatcher != nil {
		t.Fatal("expected nil dispatcher")
	}
}

func TestRedisDispatcher_EnqueueWithoutClientFails(t *testing.T) {
	dispatcher, err := NewQueue(QueueConfig{Driver: DriverRedis})
	if err == nil {
		t.Fatal("expected constructor error for missing redis addr")
	}
	if dispatcher != nil {
		t.Fatal("expected nil dispatcher")
	}
	if !strings.Contains(err.Error(), "redis addr is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedisDispatcher_BackoffUnsupported(t *testing.T) {
	dispatcher := newRedisDispatcher(fakeEnqueuer{}, false)
	err := dispatch(dispatcher,
		"job:test",
		[]byte("{}"),
		WithBackoff(time.Second),
	)
	if !errors.Is(err, ErrBackoffUnsupported) {
		t.Fatalf("expected ErrBackoffUnsupported, got %v", err)
	}
}

func TestDispatcher_ShutdownNoopForSyncAndRedis(t *testing.T) {
	syncDispatcher, err := NewQueue(QueueConfig{Driver: DriverSync})
	if err != nil {
		t.Fatalf("sync constructor failed: %v", err)
	}
	if err := syncDispatcher.Shutdown(context.Background()); err != nil {
		t.Fatalf("sync shutdown failed: %v", err)
	}

	redisDispatcher, err := NewQueue(QueueConfig{
		Driver:    DriverRedis,
		RedisAddr: "127.0.0.1:6379",
	})
	if err != nil {
		t.Fatalf("redis constructor failed: %v", err)
	}
	if err := redisDispatcher.Shutdown(context.Background()); err != nil {
		t.Fatalf("redis shutdown failed: %v", err)
	}
}
