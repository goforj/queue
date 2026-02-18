//go:build integration

package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var integrationRedis struct {
	container testcontainers.Container
	addr      string
}

var integrationMySQL struct {
	container testcontainers.Container
	addr      string
}

var integrationPostgres struct {
	container testcontainers.Container
	addr      string
}

var integrationNATS struct {
	container testcontainers.Container
	url       string
}

var integrationSQS struct {
	container testcontainers.Container
	endpoint  string
	region    string
	accessKey string
	secretKey string
}

var integrationRabbitMQ struct {
	container testcontainers.Container
	url       string
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	backends := selectedIntegrationBackends()
	needsRedis := backends["redis"]
	needsMySQL := backends["mysql"]
	needsPostgres := backends["postgres"]
	needsNATS := backends["nats"]
	needsSQS := backends["sqs"]
	needsRabbitMQ := backends["rabbitmq"]

	if needsRedis {
		redisContainer, redisAddr, err := startRedisContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start redis integration container: %v\n", err)
			os.Exit(1)
		}
		integrationRedis.container = redisContainer
		integrationRedis.addr = redisAddr
	}

	if needsMySQL {
		mysqlContainer, mysqlAddr, err := startMySQLContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start mysql integration container: %v\n", err)
			if integrationRedis.container != nil {
				_ = integrationRedis.container.Terminate(ctx)
			}
			os.Exit(1)
		}
		integrationMySQL.container = mysqlContainer
		integrationMySQL.addr = mysqlAddr
	}

	if needsPostgres {
		postgresContainer, postgresAddr, err := startPostgresContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start postgres integration container: %v\n", err)
			if integrationMySQL.container != nil {
				_ = integrationMySQL.container.Terminate(ctx)
			}
			if integrationRedis.container != nil {
				_ = integrationRedis.container.Terminate(ctx)
			}
			os.Exit(1)
		}
		integrationPostgres.container = postgresContainer
		integrationPostgres.addr = postgresAddr
	}

	if needsNATS {
		natsContainer, natsURL, err := startNATSContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start nats integration container: %v\n", err)
			if integrationPostgres.container != nil {
				_ = integrationPostgres.container.Terminate(ctx)
			}
			if integrationMySQL.container != nil {
				_ = integrationMySQL.container.Terminate(ctx)
			}
			if integrationRedis.container != nil {
				_ = integrationRedis.container.Terminate(ctx)
			}
			os.Exit(1)
		}
		integrationNATS.container = natsContainer
		integrationNATS.url = natsURL
	}
	if needsSQS {
		sqsContainer, sqsEndpoint, err := startSQSContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start sqs integration container: %v\n", err)
			if integrationNATS.container != nil {
				_ = integrationNATS.container.Terminate(ctx)
			}
			if integrationPostgres.container != nil {
				_ = integrationPostgres.container.Terminate(ctx)
			}
			if integrationMySQL.container != nil {
				_ = integrationMySQL.container.Terminate(ctx)
			}
			if integrationRedis.container != nil {
				_ = integrationRedis.container.Terminate(ctx)
			}
			os.Exit(1)
		}
		integrationSQS.container = sqsContainer
		integrationSQS.endpoint = sqsEndpoint
		integrationSQS.region = "us-east-1"
		integrationSQS.accessKey = "test"
		integrationSQS.secretKey = "test"
	}
	if needsRabbitMQ {
		rabbitMQContainer, rabbitMQURL, err := startRabbitMQContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start rabbitmq integration container: %v\n", err)
			if integrationSQS.container != nil {
				_ = integrationSQS.container.Terminate(ctx)
			}
			if integrationNATS.container != nil {
				_ = integrationNATS.container.Terminate(ctx)
			}
			if integrationPostgres.container != nil {
				_ = integrationPostgres.container.Terminate(ctx)
			}
			if integrationMySQL.container != nil {
				_ = integrationMySQL.container.Terminate(ctx)
			}
			if integrationRedis.container != nil {
				_ = integrationRedis.container.Terminate(ctx)
			}
			os.Exit(1)
		}
		integrationRabbitMQ.container = rabbitMQContainer
		integrationRabbitMQ.url = rabbitMQURL
	}

	exitCode := m.Run()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if integrationPostgres.container != nil {
		if err := integrationPostgres.container.Terminate(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate postgres integration container: %v\n", err)
		}
	}
	if integrationMySQL.container != nil {
		if err := integrationMySQL.container.Terminate(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate mysql integration container: %v\n", err)
		}
	}
	if integrationRedis.container != nil {
		if err := integrationRedis.container.Terminate(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate redis integration container: %v\n", err)
		}
	}
	if integrationNATS.container != nil {
		if err := integrationNATS.container.Terminate(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate nats integration container: %v\n", err)
		}
	}
	if integrationSQS.container != nil {
		if err := integrationSQS.container.Terminate(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate sqs integration container: %v\n", err)
		}
	}
	if integrationRabbitMQ.container != nil {
		if err := integrationRabbitMQ.container.Terminate(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate rabbitmq integration container: %v\n", err)
		}
	}

	os.Exit(exitCode)
}

func selectedIntegrationBackends() map[string]bool {
	selected := map[string]bool{
		"redis":    true,
		"mysql":    true,
		"postgres": true,
		"sqlite":   true,
		"nats":     true,
		"sqs":      true,
		"rabbitmq": true,
	}
	value := strings.TrimSpace(strings.ToLower(os.Getenv("INTEGRATION_BACKEND")))
	if value == "" || value == "all" {
		return selected
	}
	for key := range selected {
		selected[key] = false
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		selected[part] = true
	}
	return selected
}

func integrationBackendEnabled(name string) bool {
	return selectedIntegrationBackends()[strings.ToLower(name)]
}

func TestRedisIntegration_EnqueueSmoke(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}
	inspector := newRedisInspector(t)
	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
	})
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}

	queueName := uniqueQueueName("redis-smoke")
	taskType := "job:smoke"
	payload := []byte("hello")
	if err := q.Enqueue(context.Background(), NewTask(taskType).Payload(payload).OnQueue(queueName)); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	pending := waitForPendingTask(t, inspector, queueName, 3*time.Second)
	if pending.Type != taskType {
		t.Fatalf("expected task type %q, got %q", taskType, pending.Type)
	}
	if string(pending.Payload) != string(payload) {
		t.Fatalf("expected payload %q, got %q", string(payload), string(pending.Payload))
	}
}

func TestRedisIntegration_EnqueueMapsOptions(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}
	inspector := newRedisInspector(t)
	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
	})
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}

	queueName := uniqueQueueName("redis-options")
	delay := 2 * time.Second
	timeout := 7 * time.Second
	maxRetry := 4
	start := time.Now()

	err = q.Enqueue(
		context.Background(),
		NewTask("job:options").
			Payload([]byte("opts")).
			OnQueue(queueName).
			Delay(delay).
			Timeout(timeout).
			Retry(maxRetry),
	)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	scheduled := waitForScheduledTask(t, inspector, queueName, 3*time.Second)
	if scheduled.Queue != queueName {
		t.Fatalf("expected queue %q, got %q", queueName, scheduled.Queue)
	}
	if scheduled.Timeout != timeout {
		t.Fatalf("expected timeout %s, got %s", timeout, scheduled.Timeout)
	}
	if scheduled.MaxRetry != maxRetry {
		t.Fatalf("expected max retry %d, got %d", maxRetry, scheduled.MaxRetry)
	}
	if scheduled.NextProcessAt.Before(start.Add(delay - time.Second)) {
		t.Fatalf("expected next process time after delay, got %s", scheduled.NextProcessAt)
	}
}

func TestRedisIntegration_UniqueDuplicateMapsToErrDuplicate(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}
	_ = newRedisInspector(t)
	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
	})
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}

	queueName := uniqueQueueName("redis-unique")
	taskType := "job:unique"
	payload := []byte("same")
	first := NewTask(taskType).Payload(payload).OnQueue(queueName).UniqueFor(5 * time.Second)
	if err := q.Enqueue(context.Background(), first); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	second := NewTask(taskType).Payload(payload).OnQueue(queueName).UniqueFor(5 * time.Second)
	err = q.Enqueue(context.Background(), second)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestRedisIntegration_BackoffUnsupported(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}
	_ = newRedisInspector(t)
	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
	})
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}

	err = q.Enqueue(context.Background(), NewTask("job:backoff-unsupported").Payload([]byte("x")).OnQueue("default").Backoff(1*time.Second))
	if !errors.Is(err, ErrBackoffUnsupported) {
		t.Fatalf("expected ErrBackoffUnsupported, got %v", err)
	}
}

func TestRedisIntegration_BindPayloadThroughWorker(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}

	type payload struct {
		ID int `json:"id"`
	}
	received := make(chan payload, 1)

	worker, err := NewWorker(WorkerConfig{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
		Workers:   1,
	})
	if err != nil {
		t.Fatalf("new redis worker failed: %v", err)
	}
	worker.Register("job:bind", func(_ context.Context, task Task) error {
		var in payload
		if err := task.Bind(&in); err != nil {
			return err
		}
		received <- in
		return nil
	})

	startErr := make(chan error, 1)
	go func() {
		startErr <- worker.Start()
	}()
	t.Cleanup(func() { _ = worker.Shutdown() })
	time.Sleep(100 * time.Millisecond)

	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
	})
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	want := payload{ID: 99}
	if err := q.Enqueue(context.Background(), NewTask("job:bind").Payload(want).OnQueue("default")); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case got := <-received:
		if got != want {
			t.Fatalf("bind payload mismatch: got %+v want %+v", got, want)
		}
	case err := <-startErr:
		if err != nil {
			t.Fatalf("redis worker start failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for redis worker bind payload")
	}
}

func startRedisContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, net.JoinHostPort(host, port.Port()), nil
}

func startMySQLContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "mysql:8.0",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "root",
			"MYSQL_DATABASE":      "queue_test",
			"MYSQL_USER":          "queue",
			"MYSQL_PASSWORD":      "queue",
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("3306/tcp"),
			wait.ForLog("ready for connections"),
		).WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	addr := net.JoinHostPort(host, port.Port())
	if err := waitForMySQLReady(addr, 60*time.Second); err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, addr, nil
}

func startPostgresContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "queue",
			"POSTGRES_PASSWORD": "queue",
			"POSTGRES_DB":       "queue_test",
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections"),
		).WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	addr := net.JoinHostPort(host, port.Port())
	if err := waitForPostgresReady(addr, 60*time.Second); err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, addr, nil
}

func startNATSContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "nats:2-alpine",
		ExposedPorts: []string{"4222/tcp"},
		WaitingFor:   wait.ForListeningPort("4222/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	port, err := container.MappedPort(ctx, "4222/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, "nats://" + net.JoinHostPort(host, port.Port()), nil
}

func startSQSContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "localstack/localstack:3.8",
		ExposedPorts: []string{"4566/tcp"},
		Env: map[string]string{
			"SERVICES":           "sqs",
			"AWS_DEFAULT_REGION": "us-east-1",
		},
		WaitingFor: wait.ForListeningPort("4566/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	port, err := container.MappedPort(ctx, "4566/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, "http://" + net.JoinHostPort(host, port.Port()), nil
}

func startRabbitMQContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "rabbitmq:3-alpine",
		ExposedPorts: []string{"5672/tcp"},
		WaitingFor:   wait.ForListeningPort("5672/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	port, err := container.MappedPort(ctx, "5672/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, "amqp://guest:guest@" + net.JoinHostPort(host, port.Port()) + "/", nil
}

func waitForMySQLReady(addr string, timeout time.Duration) error {
	dsn := fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", addr)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			pingErr := db.Ping()
			_ = db.Close()
			if pingErr == nil {
				return nil
			}
			lastErr = pingErr
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("mysql not ready: %w", lastErr)
}

func waitForPostgresReady(addr string, timeout time.Duration) error {
	dsn := fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", addr)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		db, err := sql.Open("pgx", dsn)
		if err == nil {
			pingErr := db.Ping()
			_ = db.Close()
			if pingErr == nil {
				return nil
			}
			lastErr = pingErr
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres not ready: %w", lastErr)
}

func newRedisInspector(t *testing.T) *asynq.Inspector {
	t.Helper()
	redisOpt := asynq.RedisClientOpt{Addr: integrationRedis.addr}
	inspector := asynq.NewInspector(redisOpt)
	t.Cleanup(func() {
		_ = inspector.Close()
	})
	return inspector
}

func uniqueQueueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func waitForPendingTask(t *testing.T, inspector *asynq.Inspector, queueName string, timeout time.Duration) *asynq.TaskInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks, err := inspector.ListPendingTasks(queueName)
		if err != nil {
			t.Fatalf("list pending tasks failed: %v", err)
		}
		if len(tasks) > 0 {
			return tasks[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending task not found for queue %q within %s", queueName, timeout)
	return nil
}

func waitForScheduledTask(t *testing.T, inspector *asynq.Inspector, queueName string, timeout time.Duration) *asynq.TaskInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks, err := inspector.ListScheduledTasks(queueName)
		if err != nil {
			t.Fatalf("list scheduled tasks failed: %v", err)
		}
		if len(tasks) > 0 {
			return tasks[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("scheduled task not found for queue %q within %s", queueName, timeout)
	return nil
}

type hardeningFixture struct {
	name      string
	queueName string
	newQueue  func(t *testing.T) Queue
	newWorker func(t *testing.T) Worker

	supportsBackoff              bool
	forceTimeout                 bool
	supportsRestart              bool
	supportsPoisonRetry          bool
	supportsEnqueueCtxCancel     bool
	supportsDeterministicNoDupes bool
}

type hardeningPayload struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestIntegrationHardening_AllBackends(t *testing.T) {
	fixtures := []hardeningFixture{
		{
			name:      "redis",
			queueName: "default",
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
			newWorker: func(t *testing.T) Worker {
				w, err := NewWorker(WorkerConfig{
					Driver:    DriverRedis,
					RedisAddr: integrationRedis.addr,
					Workers:   4,
				})
				if err != nil {
					t.Fatalf("new redis worker failed: %v", err)
				}
				return w
			},
			supportsBackoff: false,
			forceTimeout:    true,
			supportsRestart: true,
			supportsPoisonRetry: false,
			supportsEnqueueCtxCancel: false,
			supportsDeterministicNoDupes: true,
		},
		{
			name:      "mysql",
			queueName: "hardening_mysql",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "mysql",
					DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
				})
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) Worker {
				w, err := NewWorker(WorkerConfig{
					Driver:         DriverDatabase,
					DatabaseDriver: "mysql",
					DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
					Workers:        4,
					PollInterval:   10 * time.Millisecond,
					DefaultQueue:   "hardening_mysql",
				})
				if err != nil {
					t.Fatalf("new mysql worker failed: %v", err)
				}
				return w
			},
			supportsBackoff: true,
			supportsRestart: true,
			supportsPoisonRetry: true,
			supportsEnqueueCtxCancel: true,
			supportsDeterministicNoDupes: true,
		},
		{
			name:      "postgres",
			queueName: "hardening_postgres",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "pgx",
					DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
				})
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) Worker {
				w, err := NewWorker(WorkerConfig{
					Driver:         DriverDatabase,
					DatabaseDriver: "pgx",
					DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
					Workers:        4,
					PollInterval:   10 * time.Millisecond,
					DefaultQueue:   "hardening_postgres",
				})
				if err != nil {
					t.Fatalf("new postgres worker failed: %v", err)
				}
				return w
			},
			supportsBackoff: true,
			supportsRestart: true,
			supportsPoisonRetry: true,
			supportsEnqueueCtxCancel: true,
			supportsDeterministicNoDupes: true,
		},
		{
			name:      "sqlite",
			queueName: "hardening_sqlite",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    fmt.Sprintf("%s/hardening-%d.db", t.TempDir(), time.Now().UnixNano()),
				})
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) Worker {
				// Use the same DSN for queue+worker in the test body.
				t.Fatal("sqlite worker fixture must be created from test-local DSN")
				return nil
			},
			supportsBackoff: true,
			supportsRestart: false,
			supportsPoisonRetry: true,
			supportsEnqueueCtxCancel: true,
			supportsDeterministicNoDupes: true,
		},
		{
			name:      "nats",
			queueName: "hardening_nats",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:  DriverNATS,
					NATSURL: integrationNATS.url,
				})
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) Worker {
				w, err := NewWorker(WorkerConfig{
					Driver:  DriverNATS,
					NATSURL: integrationNATS.url,
				})
				if err != nil {
					t.Fatalf("new nats worker failed: %v", err)
				}
				return w
			},
			supportsBackoff: true,
			supportsRestart: false,
			supportsPoisonRetry: true,
			supportsEnqueueCtxCancel: false,
			supportsDeterministicNoDupes: false,
		},
		{
			name:      "sqs",
			queueName: "hardening_sqs",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:       DriverSQS,
					SQSEndpoint:  integrationSQS.endpoint,
					SQSRegion:    integrationSQS.region,
					SQSAccessKey: integrationSQS.accessKey,
					SQSSecretKey: integrationSQS.secretKey,
				})
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) Worker {
				w, err := NewWorker(WorkerConfig{
					Driver:       DriverSQS,
					SQSEndpoint:  integrationSQS.endpoint,
					SQSRegion:    integrationSQS.region,
					SQSAccessKey: integrationSQS.accessKey,
					SQSSecretKey: integrationSQS.secretKey,
					DefaultQueue: "hardening_sqs",
				})
				if err != nil {
					t.Fatalf("new sqs worker failed: %v", err)
				}
				return w
			},
			supportsBackoff: true,
			supportsRestart: false,
			supportsPoisonRetry: true,
			supportsEnqueueCtxCancel: true,
			supportsDeterministicNoDupes: true,
		},
		{
			name:      "rabbitmq",
			queueName: "hardening_rabbitmq",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:      DriverRabbitMQ,
					RabbitMQURL: integrationRabbitMQ.url,
				})
				if err != nil {
					t.Fatalf("new rabbitmq queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) Worker {
				w, err := NewWorker(WorkerConfig{
					Driver:       DriverRabbitMQ,
					RabbitMQURL:  integrationRabbitMQ.url,
					DefaultQueue: "hardening_rabbitmq",
				})
				if err != nil {
					t.Fatalf("new rabbitmq worker failed: %v", err)
				}
				return w
			},
			supportsBackoff: true,
			supportsRestart: true,
			supportsPoisonRetry: true,
			supportsEnqueueCtxCancel: false,
			supportsDeterministicNoDupes: true,
		},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if !integrationBackendEnabled(fx.name) {
				t.Skipf("%s integration backend not selected", fx.name)
			}

			// SQLite needs a shared DSN between producer and worker; build it inline.
			if fx.name == "sqlite" {
				dsn := fmt.Sprintf("%s/hardening-%d.db", t.TempDir(), time.Now().UnixNano())
				fx.newQueue = func(t *testing.T) Queue {
					q, err := New(Config{
						Driver:         DriverDatabase,
						DatabaseDriver: "sqlite",
						DatabaseDSN:    dsn,
					})
					if err != nil {
						t.Fatalf("new sqlite queue failed: %v", err)
					}
					return q
				}
				fx.newWorker = func(t *testing.T) Worker {
					w, err := NewWorker(WorkerConfig{
						Driver:         DriverDatabase,
						DatabaseDriver: "sqlite",
						DatabaseDSN:    dsn,
						Workers:        4,
						PollInterval:   10 * time.Millisecond,
						DefaultQueue:   "hardening_sqlite",
					})
					if err != nil {
						t.Fatalf("new sqlite worker failed: %v", err)
					}
					return w
				}
				fx.supportsBackoff = true
				fx.supportsRestart = true
				fx.supportsPoisonRetry = true
				fx.supportsEnqueueCtxCancel = true
				fx.supportsDeterministicNoDupes = true
			}

			runIntegrationHardeningSuite(t, fx)
		})
	}
}

func runIntegrationHardeningSuite(t *testing.T, fx hardeningFixture) {
	t.Helper()
	q := fx.newQueue(t)
	w := fx.newWorker(t)
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
	t.Cleanup(func() { _ = w.Shutdown() })

	taskType := "job:hardening:" + fx.name
	total := int32(40)
	taskTimeout := 250 * time.Millisecond
	if fx.forceTimeout {
		taskTimeout = 2 * time.Second
	}
	var seen atomic.Int32
	var expected atomic.Int32

	t.Run("step_register_handler", func(t *testing.T) {
		w.Register(taskType, func(_ context.Context, task Task) error {
			var payload hardeningPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			seen.Add(1)
			return nil
		})
	})

	t.Run("step_start_idempotent", func(t *testing.T) {
		requireStepNoErr(t, "worker_start", w.Start())
		requireStepNoErr(t, "worker_start_idempotent", w.Start())
	})

	t.Run("step_enqueue_burst", func(t *testing.T) {
		var wg sync.WaitGroup
		errCh := make(chan error, total)
		for i := 0; i < int(total); i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				task := NewTask(taskType).
					Payload(hardeningPayload{ID: i, Name: fx.name}).
					OnQueue(fx.queueName)
				if i%3 == 0 {
					task = task.Delay(50 * time.Millisecond)
				}
				if i%4 == 0 {
					task = task.Timeout(taskTimeout)
				}
				if fx.forceTimeout && i%4 != 0 {
					task = task.Timeout(taskTimeout)
				}
				if fx.supportsBackoff && i%5 == 0 {
					task = task.Retry(1).Backoff(20 * time.Millisecond)
				}
				if err := q.Enqueue(context.Background(), task); err != nil {
					errCh <- fmt.Errorf("enqueue %d failed: %w", i, err)
					return
				}
				expected.Add(1)
			}()
		}
		wg.Wait()
		close(errCh)

		var failures int
		var first error
		for err := range errCh {
			failures++
			if first == nil {
				first = err
			}
		}
		requireStepTrue(t, "enqueue_has_success", expected.Load() > 0, "all enqueue operations failed")
		requireStepTrue(t, "enqueue_all_success", failures == 0, "failures=%d first=%v", failures, first)
	})

	t.Run("step_wait_all_processed", func(t *testing.T) {
		want := expected.Load()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if seen.Load() == want {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireStepTrue(t, "all_processed", seen.Load() == want, "processed=%d expected=%d", seen.Load(), want)
	})

	t.Run("step_poison_message_max_retry", func(t *testing.T) {
		poisonType := "job:hardening:poison:" + fx.name
		goodType := "job:hardening:poison-recovery:" + fx.name
		var poisonCalls atomic.Int32
		recoveryDone := make(chan struct{}, 1)

		w.Register(poisonType, func(_ context.Context, _ Task) error {
			poisonCalls.Add(1)
			return errors.New("poison")
		})
		w.Register(goodType, func(_ context.Context, _ Task) error {
			select {
			case recoveryDone <- struct{}{}:
			default:
			}
			return nil
		})

		poison := NewTask(poisonType).
			Payload(hardeningPayload{ID: 9001, Name: "poison"}).
			OnQueue(fx.queueName).
			Retry(2)
		if fx.forceTimeout {
			poison = poison.Timeout(taskTimeout)
		}
		if fx.supportsBackoff {
			poison = poison.Backoff(20 * time.Millisecond)
		}
		requireStepNoErr(t, "poison_enqueue", q.Enqueue(context.Background(), poison))

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if poisonCalls.Load() >= 3 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		expectedPoisonCalls := int32(3)
		if !fx.supportsPoisonRetry {
			expectedPoisonCalls = 1
		}
		requireStepTrue(
			t,
			"poison_retry_limit",
			poisonCalls.Load() == expectedPoisonCalls,
			"calls=%d expected=%d",
			poisonCalls.Load(),
			expectedPoisonCalls,
		)

		good := NewTask(goodType).
			Payload(hardeningPayload{ID: 9002, Name: "recovery"}).
			OnQueue(fx.queueName)
		requireStepNoErr(t, "poison_recovery_enqueue", q.Enqueue(context.Background(), good))

		select {
		case <-recoveryDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("[poison_recovery_processing] recovery task was not processed")
		}
	})

	t.Run("step_worker_restart_recovery", func(t *testing.T) {
		if !fx.supportsRestart {
			t.Skip("backend does not provide deterministic restart durability in this runtime")
		}
		requireStepNoErr(t, "restart_step_worker_start", w.Start())

		restartType := "job:hardening:restart:" + fx.name
		done := make(chan struct{}, 1)

		w.Register(restartType, func(_ context.Context, _ Task) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})

		task := NewTask(restartType).
			Payload(hardeningPayload{ID: 9100, Name: "restart"}).
			OnQueue(fx.queueName).
			Delay(750 * time.Millisecond)
		if fx.forceTimeout {
			task = task.Timeout(taskTimeout)
		}
		if fx.supportsBackoff {
			task = task.Retry(1).Backoff(250 * time.Millisecond)
		}
		requireStepNoErr(t, "restart_enqueue", q.Enqueue(context.Background(), task))

		requireStepNoErr(t, "restart_shutdown_first_worker", w.Shutdown())
		w = fx.newWorker(t)
		w.Register(restartType, func(_ context.Context, _ Task) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})
		requireStepNoErr(t, "restart_start_second_worker", w.Start())

		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Fatalf("[restart_recovery_processing] task did not recover after worker restart")
		}
	})

	t.Run("step_bind_invalid_json", func(t *testing.T) {
		requireStepNoErr(t, "bind_step_worker_start", w.Start())

		badType := "job:hardening:bind-bad:" + fx.name
		emptyType := "job:hardening:bind-empty:" + fx.name
		goodType := "job:hardening:bind-good:" + fx.name
		var badBindErrs atomic.Int32
		var emptyBindErrs atomic.Int32
		goodDone := make(chan struct{}, 1)

		w.Register(badType, func(_ context.Context, task Task) error {
			var payload hardeningPayload
			if err := task.Bind(&payload); err != nil {
				badBindErrs.Add(1)
			}
			return nil
		})
		w.Register(emptyType, func(_ context.Context, task Task) error {
			var payload hardeningPayload
			if err := task.Bind(&payload); err != nil {
				emptyBindErrs.Add(1)
			}
			return nil
		})
		w.Register(goodType, func(_ context.Context, task Task) error {
			var payload hardeningPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			select {
			case goodDone <- struct{}{}:
			default:
			}
			return nil
		})

		badTask := NewTask(badType).
			Payload([]byte("not-json")).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			badTask = badTask.Timeout(taskTimeout)
		}
		requireStepNoErr(t, "bind_bad_enqueue", q.Enqueue(context.Background(), badTask))

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if badBindErrs.Load() >= 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireStepTrue(t, "bind_bad_seen", badBindErrs.Load() == 1, "bind_errors=%d expected=1", badBindErrs.Load())

		emptyTask := NewTask(emptyType).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			emptyTask = emptyTask.Timeout(taskTimeout)
		}
		requireStepNoErr(t, "bind_empty_enqueue", q.Enqueue(context.Background(), emptyTask))

		deadline = time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if emptyBindErrs.Load() >= 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireStepTrue(t, "bind_empty_seen", emptyBindErrs.Load() == 1, "bind_errors=%d expected=1", emptyBindErrs.Load())

		goodTask := NewTask(goodType).
			Payload(hardeningPayload{ID: 9200, Name: "bind-good"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			goodTask = goodTask.Timeout(taskTimeout)
		}
		requireStepNoErr(t, "bind_good_enqueue", q.Enqueue(context.Background(), goodTask))

		select {
		case <-goodDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("[bind_good_processing] valid payload task was not processed")
		}
	})

	t.Run("step_unique_queue_scope", func(t *testing.T) {
		uniqueType := "job:hardening:unique-scope:" + fx.name
		payload := hardeningPayload{ID: 9300, Name: "unique-scope"}
		primary := fx.queueName
		secondary := fx.queueName + "_other"

		first := NewTask(uniqueType).
			Payload(payload).
			OnQueue(primary).
			UniqueFor(2 * time.Second)
		secondSameQueue := NewTask(uniqueType).
			Payload(payload).
			OnQueue(primary).
			UniqueFor(2 * time.Second)
		otherQueue := NewTask(uniqueType).
			Payload(payload).
			OnQueue(secondary).
			UniqueFor(2 * time.Second)
		if fx.forceTimeout {
			first = first.Timeout(taskTimeout)
			secondSameQueue = secondSameQueue.Timeout(taskTimeout)
			otherQueue = otherQueue.Timeout(taskTimeout)
		}

		requireStepNoErr(t, "unique_first_enqueue", q.Enqueue(context.Background(), first))
		dupErr := q.Enqueue(context.Background(), secondSameQueue)
		requireStepTrue(t, "unique_duplicate_rejected", errors.Is(dupErr, ErrDuplicate), "expected ErrDuplicate, got %v", dupErr)
		requireStepNoErr(t, "unique_other_queue_enqueue", q.Enqueue(context.Background(), otherQueue))
	})

	t.Run("step_enqueue_context_cancellation", func(t *testing.T) {
		requireStepNoErr(t, "enqueue_ctx_worker_start", w.Start())

		cancelType := "job:hardening:ctx-cancel:" + fx.name
		goodType := "job:hardening:ctx-good:" + fx.name
		var cancelSeen atomic.Int32
		goodDone := make(chan struct{}, 1)

		w.Register(cancelType, func(_ context.Context, _ Task) error {
			cancelSeen.Add(1)
			return nil
		})
		w.Register(goodType, func(_ context.Context, _ Task) error {
			select {
			case goodDone <- struct{}{}:
			default:
			}
			return nil
		})

		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelTask := NewTask(cancelType).
			Payload(hardeningPayload{ID: 9400, Name: "ctx-cancel"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			cancelTask = cancelTask.Timeout(taskTimeout)
		}
		err := q.Enqueue(cancelCtx, cancelTask)
		if fx.supportsEnqueueCtxCancel {
			requireStepTrue(
				t,
				"enqueue_ctx_cancel_err",
				errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded),
				"expected context cancellation error, got %v",
				err,
			)
			time.Sleep(250 * time.Millisecond)
			requireStepTrue(t, "enqueue_ctx_cancel_not_processed", cancelSeen.Load() == 0, "unexpected processed count=%d", cancelSeen.Load())
		}

		goodTask := NewTask(goodType).
			Payload(hardeningPayload{ID: 9401, Name: "ctx-good"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			goodTask = goodTask.Timeout(taskTimeout)
		}
		requireStepNoErr(t, "enqueue_ctx_good_enqueue", q.Enqueue(context.Background(), goodTask))
		select {
		case <-goodDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("[enqueue_ctx_good_processed] follow-up task was not processed")
		}
	})

	t.Run("step_shutdown_during_delay_retry", func(t *testing.T) {
		if !fx.supportsRestart {
			t.Skip("backend does not provide deterministic restart durability in this runtime")
		}
		requireStepNoErr(t, "shutdown_delay_worker_start", w.Start())

		delayedType := "job:hardening:shutdown-delay:" + fx.name
		retryType := "job:hardening:shutdown-retry:" + fx.name
		delayedDone := make(chan struct{}, 1)
		retryDone := make(chan struct{}, 1)
		var retryCalls atomic.Int32

		w.Register(delayedType, func(_ context.Context, _ Task) error {
			select {
			case delayedDone <- struct{}{}:
			default:
			}
			return nil
		})
		w.Register(retryType, func(_ context.Context, _ Task) error {
			if retryCalls.Add(1) == 1 {
				return errors.New("retry once")
			}
			select {
			case retryDone <- struct{}{}:
			default:
			}
			return nil
		})

		delayedTask := NewTask(delayedType).
			Payload(hardeningPayload{ID: 9500, Name: "shutdown-delay"}).
			OnQueue(fx.queueName).
			Delay(1200 * time.Millisecond)
		var retryTask Task
		retryEnabled := fx.supportsBackoff
		if retryEnabled {
			retryTask = NewTask(retryType).
				Payload(hardeningPayload{ID: 9501, Name: "shutdown-retry"}).
				OnQueue(fx.queueName).
				Delay(900 * time.Millisecond).
				Retry(1)
			retryTask = retryTask.Backoff(300 * time.Millisecond)
		}
		if fx.forceTimeout {
			delayedTask = delayedTask.Timeout(taskTimeout)
			if retryEnabled {
				retryTask = retryTask.Timeout(taskTimeout)
			}
		}
		requireStepNoErr(t, "shutdown_delay_enqueue", q.Enqueue(context.Background(), delayedTask))
		if retryEnabled {
			requireStepNoErr(t, "shutdown_retry_enqueue", q.Enqueue(context.Background(), retryTask))
		}

		requireStepNoErr(t, "shutdown_during_delay", w.Shutdown())

		w = fx.newWorker(t)
		w.Register(delayedType, func(_ context.Context, _ Task) error {
			select {
			case delayedDone <- struct{}{}:
			default:
			}
			return nil
		})
		w.Register(retryType, func(_ context.Context, _ Task) error {
			if retryCalls.Add(1) == 1 {
				return errors.New("retry once")
			}
			select {
			case retryDone <- struct{}{}:
			default:
			}
			return nil
		})
		requireStepNoErr(t, "shutdown_delay_restart_worker", w.Start())

		select {
		case <-delayedDone:
		case <-time.After(15 * time.Second):
			t.Fatalf("[shutdown_delay_processed] delayed task did not process after restart")
		}
		if retryEnabled {
			select {
			case <-retryDone:
			case <-time.After(15 * time.Second):
				t.Fatalf("[shutdown_retry_processed] retry task did not process after restart")
			}
		}
	})

	t.Run("step_multi_worker_contention", func(t *testing.T) {
		if !fx.supportsDeterministicNoDupes {
			t.Skip("backend does not provide deterministic no-duplication guarantees for this scenario")
		}
		requireStepNoErr(t, "multi_worker_primary_start", w.Start())

		worker2 := fx.newWorker(t)
		t.Cleanup(func() { _ = worker2.Shutdown() })

		taskType := "job:hardening:multi-worker:" + fx.name
		var mu sync.Mutex
		seenByID := make(map[int]int)
		var processed atomic.Int32
		const totalTasks = 30

		handler := func(_ context.Context, task Task) error {
			var payload hardeningPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			mu.Lock()
			seenByID[payload.ID]++
			mu.Unlock()
			processed.Add(1)
			return nil
		}
		w.Register(taskType, handler)
		worker2.Register(taskType, handler)
		requireStepNoErr(t, "multi_worker_secondary_start", worker2.Start())

		for i := 0; i < totalTasks; i++ {
			task := NewTask(taskType).
				Payload(hardeningPayload{ID: i, Name: "multi-worker"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				task = task.Timeout(taskTimeout)
			}
			requireStepNoErr(t, "multi_worker_enqueue", q.Enqueue(context.Background(), task))
		}

		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if processed.Load() >= totalTasks {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireStepTrue(t, "multi_worker_processed_all", processed.Load() >= totalTasks, "processed=%d expected>=%d", processed.Load(), totalTasks)

		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		for i := 0; i < totalTasks; i++ {
			if seenByID[i] != 1 {
				t.Fatalf("[multi_worker_no_duplicate_success] task_id=%d count=%d expected=1", i, seenByID[i])
			}
		}
	})

	t.Run("step_shutdown_idempotent", func(t *testing.T) {
		requireStepNoErr(t, "worker_shutdown", w.Shutdown())
		requireStepNoErr(t, "worker_shutdown_idempotent", w.Shutdown())
	})
}

func requireStepNoErr(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("[%s] %v", step, err)
	}
}

func requireStepTrue(t *testing.T, step string, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf("[%s] "+format, append([]any{step}, args...)...)
	}
}
