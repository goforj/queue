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
	q, err := NewSync()
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverSync {
		t.Fatalf("expected sync driver, got %q", queueDriver(q))
	}
}

func TestNewNullQueue(t *testing.T) {
	q, err := NewNull()
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverNull {
		t.Fatalf("expected null driver, got %q", queueDriver(q))
	}
}

func TestNewWorkerpoolQueue(t *testing.T) {
	q, err := NewWorkerpool()
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverWorkerpool {
		t.Fatalf("expected workerpool driver, got %q", queueDriver(q))
	}
}

func TestNewRedisQueue(t *testing.T) {
	q, err := NewRedis("127.0.0.1:6379")
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverRedis {
		t.Fatalf("expected redis driver, got %q", queueDriver(q))
	}
}

func TestNewNATSQueue(t *testing.T) {
	q, err := NewNATS("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverNATS {
		t.Fatalf("expected nats driver, got %q", queueDriver(q))
	}
}

func TestNewSQSQueue(t *testing.T) {
	q, err := NewSQS("us-east-1")
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverSQS {
		t.Fatalf("expected sqs driver, got %q", queueDriver(q))
	}
}

func TestNewRabbitMQQueue(t *testing.T) {
	q, err := NewRabbitMQ("amqp://guest:guest@127.0.0.1:5672/")
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverRabbitMQ {
		t.Fatalf("expected rabbitmq driver, got %q", queueDriver(q))
	}
}

func TestNewDatabaseQueue(t *testing.T) {
	q, err := NewDatabase("sqlite", t.TempDir()+"/queue.db")
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverDatabase {
		t.Fatalf("expected database driver, got %q", queueDriver(q))
	}
}

func TestNew_SelectsByConfig(t *testing.T) {
	testCases := []struct {
		name   string
		cfg    Config
		driver Driver
	}{
		{name: "null", cfg: Config{Driver: DriverNull}, driver: DriverNull},
		{name: "sync", cfg: Config{Driver: DriverSync}, driver: DriverSync},
		{name: "workerpool", cfg: Config{Driver: DriverWorkerpool}, driver: DriverWorkerpool},
		{
			name: "database",
			cfg: Config{
				Driver:         DriverDatabase,
				DatabaseDriver: "sqlite",
				DatabaseDSN:    t.TempDir() + "/queue.db",
			},
			driver: DriverDatabase,
		},
		{name: "redis", cfg: Config{Driver: DriverRedis, RedisAddr: "127.0.0.1:6379"}, driver: DriverRedis},
		{name: "nats", cfg: Config{Driver: DriverNATS, NATSURL: "nats://127.0.0.1:4222"}, driver: DriverNATS},
		{name: "sqs", cfg: Config{Driver: DriverSQS, SQSRegion: "us-east-1"}, driver: DriverSQS},
		{name: "rabbitmq", cfg: Config{Driver: DriverRabbitMQ, RabbitMQURL: "amqp://guest:guest@127.0.0.1:5672/"}, driver: DriverRabbitMQ},
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

func TestRedisQueue_DispatchWithoutClientFails(t *testing.T) {
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

func TestNATSQueue_DispatchWithoutURLFails(t *testing.T) {
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

func TestRabbitMQQueue_DispatchWithoutURLFails(t *testing.T) {
	q, err := New(Config{Driver: DriverRabbitMQ})
	if err == nil {
		t.Fatal("expected constructor error for missing rabbitmq url")
	}
	if q != nil {
		t.Fatal("expected nil q")
	}
	if !strings.Contains(err.Error(), "rabbitmq url is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedisQueue_BackoffUnsupported(t *testing.T) {
	q := newRedisQueue(fakeEnqueuer{}, nil, false)
	err := q.Dispatch(context.Background(), NewTask("job:test").Payload([]byte("{}")).OnQueue("default").Backoff(time.Second))
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

func TestQueueRuntime_StartWorkersFromQueueConfig(t *testing.T) {
	q, err := New(Config{
		Driver: DriverWorkerpool,
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers from queue failed: %v", err)
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
}

func TestQueueRuntime_StartWorkers_Idempotent(t *testing.T) {
	q, err := New(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	if err := q.Workers(2).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := q.Workers(3).StartWorkers(context.Background()); err != nil {
		t.Fatalf("second start workers failed: %v", err)
	}
}

func TestQueueRuntime_StartWorkersSharesInProcessRuntime(t *testing.T) {
	q, err := New(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	handled := false
	q.Register("job:shared", func(_ context.Context, _ Task) error {
		handled = true
		return nil
	})
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := q.DispatchCtx(context.Background(), NewTask("job:shared").Payload([]byte(`{}`)).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if !handled {
		t.Fatal("expected handler to run on shared runtime")
	}
}

func TestQueueRuntime_PathInvariant_NativeRuntimeSelected(t *testing.T) {
	q, err := New(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	native, ok := q.(*nativeQueueRuntime)
	if !ok {
		t.Fatalf("expected *nativeQueueRuntime, got %T", q)
	}
	if native.runtime == nil {
		t.Fatal("expected native runtime backend to be set")
	}
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if !native.started {
		t.Fatal("expected native runtime to be marked started")
	}
}

func TestQueueRuntime_PathInvariant_ExternalRuntimeSelected(t *testing.T) {
	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: "127.0.0.1:6379",
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	external, ok := q.(*externalQueueRuntime)
	if !ok {
		t.Fatalf("expected *externalQueueRuntime, got %T", q)
	}
	if external.worker != nil {
		t.Fatal("expected external worker to be nil before StartWorkers")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.StartWorkers(ctx); err == nil {
		t.Fatal("expected canceled StartWorkers to fail for external runtime")
	}
	if external.worker != nil {
		t.Fatal("expected external worker to remain nil after canceled StartWorkers")
	}
}
