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

func TestNewSyncQueue(t *testing.T) {
	q, err := New(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverSync {
		t.Fatalf("expected sync driver, got %q", queueDriver(q))
	}
}

func TestNewWorkerpoolQueue(t *testing.T) {
	q, err := New(Config{
		Driver: DriverWorkerpool,
	})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverWorkerpool {
		t.Fatalf("expected workerpool driver, got %q", queueDriver(q))
	}
}

func TestNewRedisQueue(t *testing.T) {
	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: "127.0.0.1:6379",
	})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverRedis {
		t.Fatalf("expected redis driver, got %q", queueDriver(q))
	}
}

func TestNewNATSQueue(t *testing.T) {
	q, err := New(Config{
		Driver:  DriverNATS,
		NATSURL: "nats://127.0.0.1:4222",
	})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverNATS {
		t.Fatalf("expected nats driver, got %q", queueDriver(q))
	}
}

func TestNewSQSQueue(t *testing.T) {
	q, err := New(Config{
		Driver:    DriverSQS,
		SQSRegion: "us-east-1",
	})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverSQS {
		t.Fatalf("expected sqs driver, got %q", queueDriver(q))
	}
}

func TestNew_SelectsByConfig(t *testing.T) {
	testCases := []struct {
		name   string
		cfg    Config
		driver Driver
	}{
		{name: "sync", cfg: Config{Driver: DriverSync}, driver: DriverSync},
		{name: "workerpool", cfg: Config{Driver: DriverWorkerpool}, driver: DriverWorkerpool},
		{name: "redis", cfg: Config{Driver: DriverRedis, RedisAddr: "127.0.0.1:6379"}, driver: DriverRedis},
		{name: "nats", cfg: Config{Driver: DriverNATS, NATSURL: "nats://127.0.0.1:4222"}, driver: DriverNATS},
		{name: "sqs", cfg: Config{Driver: DriverSQS, SQSRegion: "us-east-1"}, driver: DriverSQS},
		{
			name: "database",
			cfg: Config{
				Driver:         DriverDatabase,
				DatabaseDriver: "sqlite",
				DatabaseDSN:    t.TempDir() + "/queue.db",
			},
			driver: DriverDatabase,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := New(tc.cfg)
			if err != nil {
				t.Fatalf("new q failed: %v", err)
			}
			if queueDriver(q) != tc.driver {
				t.Fatalf("expected %q driver, got %q", tc.driver, queueDriver(q))
			}
		})
	}
}

func TestNew_UnknownDriverFails(t *testing.T) {
	q, err := New(Config{Driver: Driver("unknown")})
	if err == nil {
		t.Fatal("expected unknown driver error")
	}
	if q != nil {
		t.Fatal("expected nil q")
	}
}

func TestRedisQueue_EnqueueWithoutClientFails(t *testing.T) {
	q, err := New(Config{Driver: DriverRedis})
	if err == nil {
		t.Fatal("expected constructor error for missing redis addr")
	}
	if q != nil {
		t.Fatal("expected nil q")
	}
	if !strings.Contains(err.Error(), "redis addr is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNATSQueue_EnqueueWithoutURLFails(t *testing.T) {
	q, err := New(Config{Driver: DriverNATS})
	if err == nil {
		t.Fatal("expected constructor error for missing nats url")
	}
	if q != nil {
		t.Fatal("expected nil q")
	}
	if !strings.Contains(err.Error(), "nats url is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedisQueue_BackoffUnsupported(t *testing.T) {
	q := newRedisQueue(fakeEnqueuer{}, false)
	err := q.Enqueue(context.Background(), NewTask("job:test").Payload([]byte("{}")).OnQueue("default").Backoff(time.Second))
	if !errors.Is(err, ErrBackoffUnsupported) {
		t.Fatalf("expected ErrBackoffUnsupported, got %v", err)
	}
}

func TestQueue_ShutdownNoopForSyncAndRedis(t *testing.T) {
	syncQueue, err := New(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("sync constructor failed: %v", err)
	}
	if err := syncQueue.Shutdown(context.Background()); err != nil {
		t.Fatalf("sync shutdown failed: %v", err)
	}

	redisQueue, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: "127.0.0.1:6379",
	})
	if err != nil {
		t.Fatalf("redis constructor failed: %v", err)
	}
	if err := redisQueue.Shutdown(context.Background()); err != nil {
		t.Fatalf("redis shutdown failed: %v", err)
	}
}
