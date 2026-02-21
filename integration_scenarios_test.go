//go:build integration

package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/hibiken/asynq"
	amqp "github.com/rabbitmq/amqp091-go"
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

func TestRedisIntegration_DispatchSmoke(t *testing.T) {
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
	jobType := "job:smoke"
	payload := []byte("hello")
	if err := q.DispatchCtx(context.Background(), NewJob(jobType).Payload(payload).OnQueue(queueName)); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	pending := waitForPendingTask(t, inspector, queueName, 3*time.Second)
	if pending.Type != jobType {
		t.Fatalf("expected job type %q, got %q", jobType, pending.Type)
	}
	if string(pending.Payload) != string(payload) {
		t.Fatalf("expected payload %q, got %q", string(payload), string(pending.Payload))
	}
}

func TestRedisIntegration_DispatchMapsOptions(t *testing.T) {
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

	err = q.DispatchCtx(
		context.Background(),
		NewJob("job:options").
			Payload([]byte("opts")).
			OnQueue(queueName).
			Delay(delay).
			Timeout(timeout).
			Retry(maxRetry),
	)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
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

func TestRedisIntegration_DefaultTimeoutApplied(t *testing.T) {
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

	queueName := uniqueQueueName("redis-default-timeout")
	err = q.DispatchCtx(
		context.Background(),
		NewJob("job:default-timeout").
			Payload([]byte("opts")).
			OnQueue(queueName),
	)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	pending := waitForPendingTask(t, inspector, queueName, 3*time.Second)
	if pending.Timeout != redisDefaultTaskTimeout {
		t.Fatalf("expected default timeout %s, got %s", redisDefaultTaskTimeout, pending.Timeout)
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
	jobType := "job:unique"
	payload := []byte("same")
	first := NewJob(jobType).Payload(payload).OnQueue(queueName).UniqueFor(5 * time.Second)
	if err := q.DispatchCtx(context.Background(), first); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	second := NewJob(jobType).Payload(payload).OnQueue(queueName).UniqueFor(5 * time.Second)
	err = q.DispatchCtx(context.Background(), second)
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

	err = q.DispatchCtx(context.Background(), NewJob("job:backoff-unsupported").Payload([]byte("x")).OnQueue("default").Backoff(1*time.Second))
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

	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
	})
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}
	q.Register("job:bind", func(_ context.Context, task Job) error {
		var in payload
		if err := task.Bind(&in); err != nil {
			return err
		}
		received <- in
		return nil
	})
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("redis queue start failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	want := payload{ID: 99}
	if err := q.DispatchCtx(context.Background(), NewJob("job:bind").Payload(want).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case got := <-received:
		if got != want {
			t.Fatalf("bind payload mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for redis worker bind payload")
	}
}

func startRedisContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "redis:7",
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
		Image:        "postgres:16",
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
		Image:        "nats:2",
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
		Image:        "rabbitmq:3",
		ExposedPorts: []string{"5672/tcp"},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("5672/tcp"),
			wait.ForLog("Server startup complete"),
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
	port, err := container.MappedPort(ctx, "5672/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	url := "amqp://guest:guest@" + net.JoinHostPort(host, port.Port()) + "/"
	if err := waitForRabbitMQReady(url, 30*time.Second); err != nil {
		_ = container.Terminate(ctx)
		return nil, "", err
	}
	return container, url, nil
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

func waitForRabbitMQReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := amqp.Dial(url)
		if err == nil {
			ch, chErr := conn.Channel()
			if chErr == nil {
				_, qErr := ch.QueueDeclare("healthcheck", false, true, false, false, nil)
				_ = ch.Close()
				_ = conn.Close()
				if qErr == nil {
					return nil
				}
				lastErr = qErr
			} else {
				lastErr = chErr
				_ = conn.Close()
			}
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("rabbitmq not ready: %w", lastErr)
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
	t.Fatalf("pending job not found for queue %q within %s", queueName, timeout)
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
	t.Fatalf("scheduled job not found for queue %q within %s", queueName, timeout)
	return nil
}

type scenarioFixture struct {
	name      string
	queueName string
	newQueue  func(t *testing.T) Queue
	newWorker func(t *testing.T) runtimeWorkerBackend

	supportsBackoff              bool
	forceTimeout                 bool
	supportsRestart              bool
	supportsPoisonRetry          bool
	supportsDispatchCtxCancel    bool
	supportsDeterministicNoDupes bool
	supportsOrderingContract     bool
	supportsBrokerFault          bool
	supportsShutdownDelayRetry   bool
}

type scenarioPayload struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestIntegrationScenarios_AllBackends(t *testing.T) {
	fixtures := []scenarioFixture{
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
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, Config{
					Driver:    DriverRedis,
					RedisAddr: integrationRedis.addr,
				}, 4)
			},
			supportsBackoff:              false,
			forceTimeout:                 true,
			supportsRestart:              true,
			supportsPoisonRetry:          false,
			supportsDispatchCtxCancel:    false,
			supportsDeterministicNoDupes: true,
			supportsOrderingContract:     true,
			supportsBrokerFault:          true,
			supportsShutdownDelayRetry:   true,
		},
		{
			name:      "mysql",
			queueName: "scenario_mysql",
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
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "mysql",
					DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
				}, 4)
			},
			supportsBackoff:              true,
			supportsRestart:              true,
			supportsPoisonRetry:          true,
			supportsDispatchCtxCancel:    true,
			supportsDeterministicNoDupes: true,
			supportsOrderingContract:     false,
			supportsBrokerFault:          false,
			supportsShutdownDelayRetry:   true,
		},
		{
			name:      "postgres",
			queueName: "scenario_postgres",
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
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "pgx",
					DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
				}, 4)
			},
			supportsBackoff:              true,
			supportsRestart:              true,
			supportsPoisonRetry:          true,
			supportsDispatchCtxCancel:    true,
			supportsDeterministicNoDupes: true,
			supportsOrderingContract:     false,
			supportsBrokerFault:          false,
			supportsShutdownDelayRetry:   true,
		},
		{
			name:      "sqlite",
			queueName: "scenario_sqlite",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    fmt.Sprintf("%s/scenario-%d.db", t.TempDir(), time.Now().UnixNano()),
				})
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				// Use the same DSN for queue+worker in the test body.
				t.Fatal("sqlite worker fixture must be created from test-local DSN")
				return nil
			},
			supportsBackoff:              true,
			supportsRestart:              false,
			supportsPoisonRetry:          true,
			supportsDispatchCtxCancel:    true,
			supportsDeterministicNoDupes: true,
			supportsOrderingContract:     false,
			supportsBrokerFault:          false,
			supportsShutdownDelayRetry:   true,
		},
		{
			name:      "nats",
			queueName: "scenario_nats",
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
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, Config{
					Driver:  DriverNATS,
					NATSURL: integrationNATS.url,
				}, 4)
			},
			supportsBackoff:              true,
			supportsRestart:              false,
			supportsPoisonRetry:          true,
			supportsDispatchCtxCancel:    false,
			supportsDeterministicNoDupes: false,
			supportsOrderingContract:     false,
			supportsBrokerFault:          false,
			supportsShutdownDelayRetry:   false,
		},
		{
			name:      "sqs",
			queueName: "scenario_sqs",
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
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, Config{
					Driver:       DriverSQS,
					SQSEndpoint:  integrationSQS.endpoint,
					SQSRegion:    integrationSQS.region,
					SQSAccessKey: integrationSQS.accessKey,
					SQSSecretKey: integrationSQS.secretKey,
				}, 4)
			},
			supportsBackoff:              true,
			supportsRestart:              false,
			supportsPoisonRetry:          true,
			supportsDispatchCtxCancel:    true,
			supportsDeterministicNoDupes: true,
			supportsOrderingContract:     false,
			supportsBrokerFault:          false,
			supportsShutdownDelayRetry:   false,
		},
		{
			name:      "rabbitmq",
			queueName: "scenario_rabbitmq",
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
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, Config{
					Driver:      DriverRabbitMQ,
					RabbitMQURL: integrationRabbitMQ.url,
				}, 4)
			},
			supportsBackoff:              true,
			supportsRestart:              true,
			supportsPoisonRetry:          true,
			supportsDispatchCtxCancel:    false,
			supportsDeterministicNoDupes: true,
			supportsOrderingContract:     false,
			supportsBrokerFault:          false,
			supportsShutdownDelayRetry:   true,
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
				dsn := fmt.Sprintf("%s/scenario-%d.db", t.TempDir(), time.Now().UnixNano())
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
				fx.newWorker = func(t *testing.T) runtimeWorkerBackend {
					return newQueueBackedWorker(t, Config{
						Driver:         DriverDatabase,
						DatabaseDriver: "sqlite",
						DatabaseDSN:    dsn,
					}, 4)
				}
				fx.supportsBackoff = true
				fx.supportsRestart = true
				fx.supportsPoisonRetry = true
				fx.supportsDispatchCtxCancel = true
				fx.supportsDeterministicNoDupes = true
				fx.supportsOrderingContract = true
				fx.supportsBrokerFault = false
				fx.supportsShutdownDelayRetry = true
			}

			runIntegrationScenariosSuite(t, fx)
		})
	}
}

func runIntegrationScenariosSuite(t *testing.T, fx scenarioFixture) {
	t.Helper()
	q := fx.newQueue(t)
	w := fx.newWorker(t)
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
	t.Cleanup(func() { _ = (w).Shutdown(context.Background()) })

	jobType := "job:scenario:" + fx.name
	total := int32(40)
	taskTimeout := 250 * time.Millisecond
	if fx.forceTimeout {
		taskTimeout = 2 * time.Second
	}
	var seen atomic.Int32
	var expected atomic.Int32

	t.Run("scenario_register_handler", func(t *testing.T) {
		w.Register(jobType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			seen.Add(1)
			return nil
		})
	})

	t.Run("scenario_startworkers_idempotent", func(t *testing.T) {
		requireScenarioNoErr(t, "worker_start", (w).StartWorkers(context.Background()))
		requireScenarioNoErr(t, "worker_start_idempotent", (w).StartWorkers(context.Background()))
	})

	t.Run("scenario_dispatch_burst", func(t *testing.T) {
		var wg sync.WaitGroup
		errCh := make(chan error, total)
		for i := 0; i < int(total); i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				task := NewJob(jobType).
					Payload(scenarioPayload{ID: i, Name: fx.name}).
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
				if err := q.DispatchCtx(context.Background(), task); err != nil {
					errCh <- fmt.Errorf("dispatch %d failed: %w", i, err)
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
		requireScenarioTrue(t, "dispatch_has_success", expected.Load() > 0, "all dispatch operations failed")
		requireScenarioTrue(t, "dispatch_all_success", failures == 0, "failures=%d first=%v", failures, first)
	})

	t.Run("scenario_wait_all_processed", func(t *testing.T) {
		want := expected.Load()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if seen.Load() == want {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "all_processed", seen.Load() == want, "processed=%d expected=%d", seen.Load(), want)
	})

	t.Run("scenario_poison_message_max_retry", func(t *testing.T) {
		poisonType := "job:scenario:poison:" + fx.name
		goodType := "job:scenario:poison-recovery:" + fx.name
		var poisonCalls atomic.Int32
		recoveryDone := make(chan struct{}, 1)

		w.Register(poisonType, func(_ context.Context, _ Job) error {
			poisonCalls.Add(1)
			return errors.New("poison")
		})
		w.Register(goodType, func(_ context.Context, _ Job) error {
			select {
			case recoveryDone <- struct{}{}:
			default:
			}
			return nil
		})

		poison := NewJob(poisonType).
			Payload(scenarioPayload{ID: 9001, Name: "poison"}).
			OnQueue(fx.queueName).
			Retry(2)
		if fx.forceTimeout {
			poison = poison.Timeout(taskTimeout)
		}
		if fx.supportsBackoff {
			poison = poison.Backoff(20 * time.Millisecond)
		}
		requireScenarioNoErr(t, "poison_dispatch", q.DispatchCtx(context.Background(), poison))

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
		requireScenarioTrue(
			t,
			"poison_retry_limit",
			poisonCalls.Load() == expectedPoisonCalls,
			"calls=%d expected=%d",
			poisonCalls.Load(),
			expectedPoisonCalls,
		)

		good := NewJob(goodType).
			Payload(scenarioPayload{ID: 9002, Name: "recovery"}).
			OnQueue(fx.queueName)
		requireScenarioNoErr(t, "poison_recovery_dispatch", q.DispatchCtx(context.Background(), good))

		select {
		case <-recoveryDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("[poison_recovery_processing] recovery job was not processed")
		}
	})

	t.Run("scenario_worker_restart_recovery", func(t *testing.T) {
		if !fx.supportsRestart {
			t.Skip("backend does not provide deterministic restart durability in this runtime")
		}
		requireScenarioNoErr(t, "restart_scenario_worker_start", (w).StartWorkers(context.Background()))

		restartType := "job:scenario:restart:" + fx.name
		done := make(chan struct{}, 1)

		w.Register(restartType, func(_ context.Context, _ Job) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})

		task := NewJob(restartType).
			Payload(scenarioPayload{ID: 9100, Name: "restart"}).
			OnQueue(fx.queueName).
			Delay(750 * time.Millisecond)
		if fx.forceTimeout {
			task = task.Timeout(taskTimeout)
		}
		if fx.supportsBackoff {
			task = task.Retry(1).Backoff(250 * time.Millisecond)
		}
		requireScenarioNoErr(t, "restart_dispatch", q.DispatchCtx(context.Background(), task))

		requireScenarioNoErr(t, "restart_shutdown_first_worker", (w).Shutdown(context.Background()))
		w = fx.newWorker(t)
		w.Register(restartType, func(_ context.Context, _ Job) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})
		requireScenarioNoErr(t, "restart_start_second_worker", (w).StartWorkers(context.Background()))

		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Fatalf("[restart_recovery_processing] job did not recover after worker restart")
		}
	})

	t.Run("scenario_bind_invalid_json", func(t *testing.T) {
		requireScenarioNoErr(t, "bind_scenario_worker_start", (w).StartWorkers(context.Background()))

		badType := "job:scenario:bind-bad:" + fx.name
		emptyType := "job:scenario:bind-empty:" + fx.name
		goodType := "job:scenario:bind-good:" + fx.name
		var badBindErrs atomic.Int32
		var emptyBindErrs atomic.Int32
		goodDone := make(chan struct{}, 1)

		w.Register(badType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				badBindErrs.Add(1)
			}
			return nil
		})
		w.Register(emptyType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				emptyBindErrs.Add(1)
			}
			return nil
		})
		w.Register(goodType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			select {
			case goodDone <- struct{}{}:
			default:
			}
			return nil
		})

		badTask := NewJob(badType).
			Payload([]byte("not-json")).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			badTask = badTask.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "bind_bad_dispatch", q.DispatchCtx(context.Background(), badTask))

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if badBindErrs.Load() >= 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "bind_bad_seen", badBindErrs.Load() == 1, "bind_errors=%d expected=1", badBindErrs.Load())

		emptyTask := NewJob(emptyType).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			emptyTask = emptyTask.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "bind_empty_dispatch", q.DispatchCtx(context.Background(), emptyTask))

		deadline = time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if emptyBindErrs.Load() >= 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "bind_empty_seen", emptyBindErrs.Load() == 1, "bind_errors=%d expected=1", emptyBindErrs.Load())

		goodTask := NewJob(goodType).
			Payload(scenarioPayload{ID: 9200, Name: "bind-good"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			goodTask = goodTask.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "bind_good_dispatch", q.DispatchCtx(context.Background(), goodTask))

		select {
		case <-goodDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("[bind_good_processing] valid payload job was not processed")
		}
	})

	t.Run("scenario_unique_queue_scope", func(t *testing.T) {
		uniqueType := "job:scenario:unique-scope:" + fx.name
		payload := scenarioPayload{ID: 9300, Name: "unique-scope"}
		primary := fx.queueName
		secondary := fx.queueName + "_other"

		first := NewJob(uniqueType).
			Payload(payload).
			OnQueue(primary).
			UniqueFor(2 * time.Second)
		secondSameQueue := NewJob(uniqueType).
			Payload(payload).
			OnQueue(primary).
			UniqueFor(2 * time.Second)
		otherQueue := NewJob(uniqueType).
			Payload(payload).
			OnQueue(secondary).
			UniqueFor(2 * time.Second)
		if fx.forceTimeout {
			first = first.Timeout(taskTimeout)
			secondSameQueue = secondSameQueue.Timeout(taskTimeout)
			otherQueue = otherQueue.Timeout(taskTimeout)
		}

		requireScenarioNoErr(t, "unique_first_dispatch", q.DispatchCtx(context.Background(), first))
		dupErr := q.DispatchCtx(context.Background(), secondSameQueue)
		requireScenarioTrue(t, "unique_duplicate_rejected", errors.Is(dupErr, ErrDuplicate), "expected ErrDuplicate, got %v", dupErr)
		requireScenarioNoErr(t, "unique_other_queue_dispatch", q.DispatchCtx(context.Background(), otherQueue))
	})

	t.Run("scenario_dispatch_context_cancellation", func(t *testing.T) {
		requireScenarioNoErr(t, "dispatch_ctx_worker_start", (w).StartWorkers(context.Background()))

		cancelType := "job:scenario:ctx-cancel:" + fx.name
		goodType := "job:scenario:ctx-good:" + fx.name
		var cancelSeen atomic.Int32
		goodDone := make(chan struct{}, 1)

		w.Register(cancelType, func(_ context.Context, _ Job) error {
			cancelSeen.Add(1)
			return nil
		})
		w.Register(goodType, func(_ context.Context, _ Job) error {
			select {
			case goodDone <- struct{}{}:
			default:
			}
			return nil
		})

		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelTask := NewJob(cancelType).
			Payload(scenarioPayload{ID: 9400, Name: "ctx-cancel"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			cancelTask = cancelTask.Timeout(taskTimeout)
		}
		err := q.DispatchCtx(cancelCtx, cancelTask)
		if fx.supportsDispatchCtxCancel {
			requireScenarioTrue(
				t,
				"dispatch_ctx_cancel_err",
				errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded),
				"expected context cancellation error, got %v",
				err,
			)
			time.Sleep(250 * time.Millisecond)
			requireScenarioTrue(t, "dispatch_ctx_cancel_not_processed", cancelSeen.Load() == 0, "unexpected processed count=%d", cancelSeen.Load())
		}

		goodTask := NewJob(goodType).
			Payload(scenarioPayload{ID: 9401, Name: "ctx-good"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			goodTask = goodTask.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "dispatch_ctx_good_dispatch", q.DispatchCtx(context.Background(), goodTask))
		select {
		case <-goodDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("[dispatch_ctx_good_processed] follow-up job was not processed")
		}
	})

	t.Run("scenario_shutdown_during_delay_retry", func(t *testing.T) {
		if !fx.supportsRestart || !fx.supportsShutdownDelayRetry {
			t.Skip("backend does not provide deterministic restart durability in this runtime")
		}
		requireScenarioNoErr(t, "shutdown_delay_worker_start", (w).StartWorkers(context.Background()))

		delayedType := "job:scenario:shutdown-delay:" + fx.name
		retryType := "job:scenario:shutdown-retry:" + fx.name
		delayedDone := make(chan struct{}, 1)
		retryDone := make(chan struct{}, 1)
		var retryCalls atomic.Int32

		w.Register(delayedType, func(_ context.Context, _ Job) error {
			select {
			case delayedDone <- struct{}{}:
			default:
			}
			return nil
		})
		w.Register(retryType, func(_ context.Context, _ Job) error {
			if retryCalls.Add(1) == 1 {
				return errors.New("retry once")
			}
			select {
			case retryDone <- struct{}{}:
			default:
			}
			return nil
		})

		delayedTask := NewJob(delayedType).
			Payload(scenarioPayload{ID: 9500, Name: "shutdown-delay"}).
			OnQueue(fx.queueName).
			Delay(1200 * time.Millisecond)
		var retryTask Job
		retryEnabled := fx.supportsBackoff
		if retryEnabled {
			retryTask = NewJob(retryType).
				Payload(scenarioPayload{ID: 9501, Name: "shutdown-retry"}).
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
		requireScenarioNoErr(t, "shutdown_delay_dispatch", q.DispatchCtx(context.Background(), delayedTask))
		if retryEnabled {
			requireScenarioNoErr(t, "shutdown_retry_dispatch", q.DispatchCtx(context.Background(), retryTask))
		}

		requireScenarioNoErr(t, "shutdown_during_delay", (w).Shutdown(context.Background()))

		w = fx.newWorker(t)
		w.Register(delayedType, func(_ context.Context, _ Job) error {
			select {
			case delayedDone <- struct{}{}:
			default:
			}
			return nil
		})
		w.Register(retryType, func(_ context.Context, _ Job) error {
			if retryCalls.Add(1) == 1 {
				return errors.New("retry once")
			}
			select {
			case retryDone <- struct{}{}:
			default:
			}
			return nil
		})
		requireScenarioNoErr(t, "shutdown_delay_restart_worker", (w).StartWorkers(context.Background()))

		select {
		case <-delayedDone:
		case <-time.After(15 * time.Second):
			t.Fatalf("[shutdown_delay_processed] delayed job did not process after restart")
		}
		if retryEnabled {
			select {
			case <-retryDone:
			case <-time.After(15 * time.Second):
				t.Fatalf("[shutdown_retry_processed] retry job did not process after restart")
			}
		}
	})

	t.Run("scenario_multi_worker_contention", func(t *testing.T) {
		if !fx.supportsDeterministicNoDupes {
			t.Skip("backend does not provide deterministic no-duplication guarantees for this scenario")
		}
		requireScenarioNoErr(t, "multi_worker_primary_start", (w).StartWorkers(context.Background()))

		worker2 := fx.newWorker(t)
		t.Cleanup(func() { _ = (worker2).Shutdown(context.Background()) })

		jobType := "job:scenario:multi-worker:" + fx.name
		var mu sync.Mutex
		seenByID := make(map[int]int)
		var processed atomic.Int32
		const totalTasks = 30

		handler := func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			mu.Lock()
			seenByID[payload.ID]++
			mu.Unlock()
			processed.Add(1)
			return nil
		}
		w.Register(jobType, handler)
		worker2.Register(jobType, handler)
		requireScenarioNoErr(t, "multi_worker_secondary_start", (worker2).StartWorkers(context.Background()))

		for i := 0; i < totalTasks; i++ {
			task := NewJob(jobType).
				Payload(scenarioPayload{ID: i, Name: "multi-worker"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				task = task.Timeout(taskTimeout)
			}
			requireScenarioNoErr(t, "multi_worker_dispatch", q.DispatchCtx(context.Background(), task))
		}

		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if processed.Load() >= totalTasks {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "multi_worker_processed_all", processed.Load() >= totalTasks, "processed=%d expected>=%d", processed.Load(), totalTasks)

		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		for i := 0; i < totalTasks; i++ {
			if seenByID[i] != 1 {
				t.Fatalf("[multi_worker_no_duplicate_success] task_id=%d count=%d expected=1", i, seenByID[i])
			}
		}
	})

	t.Run("scenario_duplicate_delivery_idempotency", func(t *testing.T) {
		requireScenarioNoErr(t, "idempotency_worker_start", (w).StartWorkers(context.Background()))

		jobType := "job:scenario:idempotency:" + fx.name
		var attempts atomic.Int32
		var committed atomic.Int32
		done := make(chan struct{}, 1)
		var mu sync.Mutex
		seen := make(map[int]struct{})

		w.Register(jobType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			attempt := attempts.Add(1)

			mu.Lock()
			if _, ok := seen[payload.ID]; !ok {
				seen[payload.ID] = struct{}{}
				committed.Add(1)
			}
			mu.Unlock()
			if attempt >= 2 {
				select {
				case done <- struct{}{}:
				default:
				}
			}
			return nil
		})

		first := NewJob(jobType).
			Payload(scenarioPayload{ID: 9600, Name: "idempotency"}).
			OnQueue(fx.queueName)
		second := NewJob(jobType).
			Payload(scenarioPayload{ID: 9600, Name: "idempotency"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			first = first.Timeout(taskTimeout)
			second = second.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "idempotency_dispatch_first", q.DispatchCtx(context.Background(), first))
		requireScenarioNoErr(t, "idempotency_dispatch_second", q.DispatchCtx(context.Background(), second))

		select {
		case <-done:
		case <-time.After(45 * time.Second):
			t.Fatalf("[idempotency_done] duplicate deliveries did not both complete")
		}
		requireScenarioTrue(t, "idempotency_attempts", attempts.Load() >= 2, "attempts=%d expected>=2", attempts.Load())
		requireScenarioTrue(t, "idempotency_side_effect_once", committed.Load() == 1, "committed=%d expected=1", committed.Load())
	})

	t.Run("scenario_dispatch_during_broker_fault", func(t *testing.T) {
		if !fx.supportsBrokerFault {
			t.Skip("backend does not support deterministic broker fault injection in this suite")
		}

		requireScenarioNoErr(t, "fault_worker_shutdown", (w).Shutdown(context.Background()))
		requireScenarioNoErr(t, "fault_queue_shutdown", q.Shutdown(context.Background()))

		qFault := fx.newQueue(t)
		defer func() { _ = qFault.Shutdown(context.Background()) }()
		stopTimeout := 10 * time.Second
		requireScenarioNoErr(t, "fault_stop_broker", integrationRedis.container.Stop(context.Background(), &stopTimeout))

		badCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		task := NewJob("job:scenario:fault-dispatch:" + fx.name).
			Payload(scenarioPayload{ID: 9700, Name: "fault-dispatch"}).
			OnQueue(fx.queueName).
			Retry(1)
		if fx.forceTimeout {
			task = task.Timeout(taskTimeout)
		}
		err := qFault.DispatchCtx(badCtx, task)
		requireScenarioTrue(t, "fault_dispatch_err", err != nil, "expected dispatch error while broker is down")

		requireScenarioNoErr(t, "fault_start_broker", integrationRedis.container.Start(context.Background()))
		requireScenarioNoErr(t, "fault_refresh_addr", refreshRedisAddr(context.Background()))
		requireScenarioNoErr(t, "fault_wait_broker", waitForTCP(integrationRedis.addr, 10*time.Second))
	})

	t.Run("scenario_consume_after_broker_recovery", func(t *testing.T) {
		if !fx.supportsBrokerFault {
			t.Skip("backend does not support deterministic broker fault injection in this suite")
		}

		requireScenarioNoErr(t, "recover_shutdown_worker", (w).Shutdown(context.Background()))
		requireScenarioNoErr(t, "recover_shutdown_queue", q.Shutdown(context.Background()))

		q = fx.newQueue(t)
		w = fx.newWorker(t)

		jobType := "job:scenario:fault-recovery:" + fx.name
		done := make(chan struct{}, 1)
		w.Register(jobType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})
		requireScenarioNoErr(t, "recover_worker_start", (w).StartWorkers(context.Background()))

		task := NewJob(jobType).
			Payload(scenarioPayload{ID: 9701, Name: "fault-recovery"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			task = task.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "recover_dispatch", q.DispatchCtx(context.Background(), task))
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Fatalf("[recover_processed] job was not processed after broker recovery")
		}
	})

	t.Run("scenario_ordering_contract", func(t *testing.T) {
		if !fx.supportsOrderingContract {
			t.Skip("backend does not expose strict ordering guarantees in this suite")
		}
		requireScenarioNoErr(t, "ordering_shutdown_previous_worker", (w).Shutdown(context.Background()))
		w = newOrderingWorker(t, fx)
		requireScenarioNoErr(t, "ordering_worker_start", (w).StartWorkers(context.Background()))

		jobType := "job:scenario:ordering:" + fx.name
		const count = 20
		orderCh := make(chan int, count)
		w.Register(jobType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			orderCh <- payload.ID
			return nil
		})

		for i := 0; i < count; i++ {
			task := NewJob(jobType).
				Payload(scenarioPayload{ID: i, Name: "ordering"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				task = task.Timeout(taskTimeout)
			}
			requireScenarioNoErr(t, "ordering_dispatch", q.DispatchCtx(context.Background(), task))
		}

		got := make([]int, 0, count)
		deadline := time.After(15 * time.Second)
		for len(got) < count {
			select {
			case id := <-orderCh:
				got = append(got, id)
			case <-deadline:
				t.Fatalf("[ordering_collect] got=%d expected=%d", len(got), count)
			}
		}
		for i := 0; i < count; i++ {
			if got[i] != i {
				t.Fatalf("[ordering_fifo] index=%d got=%d expected=%d", i, got[i], i)
			}
		}
	})

	t.Run("scenario_backpressure_saturation", func(t *testing.T) {
		requireScenarioNoErr(t, "backpressure_worker_start", (w).StartWorkers(context.Background()))

		jobType := "job:scenario:backpressure:" + fx.name
		var processed atomic.Int32
		w.Register(jobType, func(_ context.Context, _ Job) error {
			time.Sleep(40 * time.Millisecond)
			processed.Add(1)
			return nil
		})

		const total = 80
		for i := 0; i < total; i++ {
			task := NewJob(jobType).
				Payload(scenarioPayload{ID: i, Name: "backpressure"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				task = task.Timeout(taskTimeout)
			}
			requireScenarioNoErr(t, "backpressure_dispatch", q.DispatchCtx(context.Background(), task))
		}

		probeType := "job:scenario:backpressure-probe:" + fx.name
		probeDone := make(chan struct{}, 1)
		w.Register(probeType, func(_ context.Context, _ Job) error {
			select {
			case probeDone <- struct{}{}:
			default:
			}
			return nil
		})
		probe := NewJob(probeType).
			Payload(scenarioPayload{ID: 9800, Name: "probe"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			probe = probe.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "backpressure_probe_dispatch", q.DispatchCtx(context.Background(), probe))

		select {
		case <-probeDone:
		case <-time.After(20 * time.Second):
			t.Fatalf("[backpressure_probe_processed] probe job was not processed under saturation")
		}
		requireScenarioTrue(t, "backpressure_progress", processed.Load() > 0, "processed=%d expected>0", processed.Load())
	})

	t.Run("scenario_payload_large", func(t *testing.T) {
		requireScenarioNoErr(t, "payload_large_worker_start", (w).StartWorkers(context.Background()))

		jobType := "job:scenario:payload-large:" + fx.name
		done := make(chan struct{}, 1)
		expectedLen := 128 * 1024
		w.Register(jobType, func(_ context.Context, task Job) error {
			var payload struct {
				ID   int    `json:"id"`
				Data string `json:"data"`
			}
			if err := task.Bind(&payload); err != nil {
				return err
			}
			if len(payload.Data) != expectedLen {
				return fmt.Errorf("unexpected payload length: got %d expected %d", len(payload.Data), expectedLen)
			}
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})

		task := NewJob(jobType).
			Payload(struct {
				ID   int    `json:"id"`
				Data string `json:"data"`
			}{ID: 9900, Data: strings.Repeat("x", expectedLen)}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			task = task.Timeout(taskTimeout)
		}
		requireScenarioNoErr(t, "payload_large_dispatch", q.DispatchCtx(context.Background(), task))
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatalf("[payload_large_processed] large payload job was not processed")
		}
	})

	t.Run("scenario_config_option_fuzz", func(t *testing.T) {
		requireScenarioNoErr(t, "fuzz_worker_start", (w).StartWorkers(context.Background()))

		jobType := "job:scenario:fuzz:" + fx.name
		var processed atomic.Int32
		w.Register(jobType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			processed.Add(1)
			return nil
		})

		seed := int64(1337 + len(fx.name)*97)
		r := rand.New(rand.NewSource(seed))
		expected := int32(0)
		const cases = 80
		for i := 0; i < cases; i++ {
			task := NewJob(jobType).
				Payload(scenarioPayload{ID: 10000 + i, Name: "fuzz"}).
				OnQueue(fx.queueName)
			if r.Intn(2) == 0 {
				task = task.Delay(time.Duration(20+r.Intn(120)) * time.Millisecond)
			}
			if r.Intn(2) == 0 {
				timeout := time.Duration(80+r.Intn(260)) * time.Millisecond
				if fx.forceTimeout && timeout < time.Second {
					timeout = 2 * time.Second
				}
				task = task.Timeout(timeout)
			}
			retrySet := false
			if r.Intn(2) == 0 {
				task = task.Retry(r.Intn(3))
				retrySet = true
			}
			if retrySet && fx.supportsBackoff && r.Intn(2) == 0 {
				task = task.Backoff(time.Duration(30+r.Intn(160)) * time.Millisecond)
			}
			if r.Intn(4) == 0 {
				task = task.UniqueFor(time.Duration(1+r.Intn(2)) * time.Second)
			}
			if err := q.DispatchCtx(context.Background(), task); err != nil {
				t.Fatalf("[fuzz _dispatch_case_%d] dispatch failed: %v", i, err)
			}
			expected++
		}

		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if processed.Load() == expected {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "fuzz_all_processed", processed.Load() == expected, "processed=%d expected=%d", processed.Load(), expected)
	})

	t.Run("scenario_soak_mixed_load", func(t *testing.T) {
		if os.Getenv("RUN_SOAK") != "1" {
			t.Skip("set RUN_SOAK=1 to enable long-running soak step")
		}
		requireScenarioNoErr(t, "soak_worker_start", (w).StartWorkers(context.Background()))

		jobType := "job:scenario:soak:" + fx.name
		var processed atomic.Int32
		w.Register(jobType, func(_ context.Context, task Job) error {
			var payload scenarioPayload
			if err := task.Bind(&payload); err != nil {
				return err
			}
			if payload.ID%7 == 0 {
				time.Sleep(5 * time.Millisecond)
			}
			processed.Add(1)
			return nil
		})

		const total = 1600
		producers := 16
		perProducer := total / producers
		errCh := make(chan error, total)
		var wg sync.WaitGroup
		for p := 0; p < producers; p++ {
			base := p * perProducer
			wg.Add(1)
			go func(startID int) {
				defer wg.Done()
				for i := 0; i < perProducer; i++ {
					id := startID + i
					task := NewJob(jobType).
						Payload(scenarioPayload{ID: id, Name: "soak"}).
						OnQueue(fx.queueName)
					if id%5 == 0 {
						task = task.Delay(30 * time.Millisecond)
					}
					if id%6 == 0 {
						timeout := 250 * time.Millisecond
						if fx.forceTimeout {
							timeout = 2 * time.Second
						}
						task = task.Timeout(timeout)
					}
					if id%8 == 0 {
						task = task.Retry(1)
						if fx.supportsBackoff {
							task = task.Backoff(80 * time.Millisecond)
						}
					}
					if id%10 == 0 {
						task = task.UniqueFor(1 * time.Second)
					}
					if err := q.DispatchCtx(context.Background(), task); err != nil {
						errCh <- err
						return
					}
				}
			}(base)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatalf("[soak_dispatch] dispatch failed: %v", err)
		}

		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			if processed.Load() >= total {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		requireScenarioTrue(t, "soak_processed", processed.Load() >= total, "processed=%d expected>=%d", processed.Load(), total)
	})

	t.Run("scenario_shutdown_idempotent", func(t *testing.T) {
		requireScenarioNoErr(t, "worker_shutdown", (w).Shutdown(context.Background()))
		requireScenarioNoErr(t, "worker_shutdown_idempotent", (w).Shutdown(context.Background()))
	})
}

func requireScenarioNoErr(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("[%s] %v", step, err)
	}
}

func requireScenarioTrue(t *testing.T, step string, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf("[%s] "+format, append([]any{step}, args...)...)
	}
}

func waitForTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for tcp endpoint %s", addr)
}

func refreshRedisAddr(ctx context.Context) error {
	if integrationRedis.container == nil {
		return fmt.Errorf("redis integration container is not initialized")
	}
	host, err := integrationRedis.container.Host(ctx)
	if err != nil {
		return err
	}
	port, err := integrationRedis.container.MappedPort(ctx, nat.Port("6379/tcp"))
	if err != nil {
		return err
	}
	integrationRedis.addr = net.JoinHostPort(host, port.Port())
	return nil
}

type queueBackedWorker struct {
	q       Queue
	workers int
}

func newQueueBackedWorker(t *testing.T, cfg Config, workers int) runtimeWorkerBackend {
	t.Helper()
	q, err := New(cfg)
	if err != nil {
		t.Fatalf("new worker-backed queue failed: %v", err)
	}
	return &queueBackedWorker{q: q, workers: workers}
}

func (w *queueBackedWorker) Register(jobType string, handler Handler) {
	w.q.Register(jobType, handler)
}

func (w *queueBackedWorker) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return w.q.Workers(w.workers).StartWorkers(ctx)
}

func (w *queueBackedWorker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return w.q.Shutdown(ctx)
}

func newOrderingWorker(t *testing.T, fx scenarioFixture) runtimeWorkerBackend {
	t.Helper()
	switch fx.name {
	case "redis":
		return newQueueBackedWorker(t, Config{
			Driver:    DriverRedis,
			RedisAddr: integrationRedis.addr,
		}, 1)
	case "mysql":
		return newQueueBackedWorker(t, Config{
			Driver:         DriverDatabase,
			DatabaseDriver: "mysql",
			DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
		}, 1)
	case "postgres":
		return newQueueBackedWorker(t, Config{
			Driver:         DriverDatabase,
			DatabaseDriver: "pgx",
			DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
		}, 1)
	default:
		return fx.newWorker(t)
	}
}
