//go:build integration

package all_test

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
	_ "github.com/go-sql-driver/mysql"
	. "github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
	"github.com/hibiken/asynq"
	_ "github.com/jackc/pgx/v5/stdlib"
	amqp "github.com/rabbitmq/amqp091-go"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	_ "modernc.org/sqlite"
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

const redisDefaultJobTimeout = 30 * time.Second

type runtimeWorkerBackend interface {
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	backends := selectedIntegrationBackends()
	needsRedis := backends[testenv.BackendRedis]
	needsMySQL := backends[testenv.BackendMySQL]
	needsPostgres := backends[testenv.BackendPostgres]
	needsNATS := backends[testenv.BackendNATS]
	needsSQS := backends[testenv.BackendSQS]
	needsRabbitMQ := backends[testenv.BackendRabbitMQ]

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
	return testenv.SelectedBackends(os.Getenv("INTEGRATION_BACKEND"))
}

func integrationBackendEnabled(name string) bool {
	return testenv.BackendEnabled(os.Getenv("INTEGRATION_BACKEND"), name)
}

func TestRedisIntegration_DispatchSmoke(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}
	inspector := newRedisInspector(t)
	q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}

	queueName := uniqueQueueName("redis-smoke")
	jobType := "job:smoke"
	payload := []byte("hello")
	if err := q.DispatchCtx(context.Background(), NewJob(jobType).Payload(payload).OnQueue(queueName)); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	pending := waitForPendingJob(t, inspector, queueName, 3*time.Second)
	if pending.Type != jobType {
		t.Fatalf("expected job type %q, got %q", jobType, pending.Type)
	}
	if string(pending.Payload) != string(payload) {
		t.Fatalf("expected payload %q, got %q", string(payload), string(pending.Payload))
	}
}

func TestRedisIntegration_DispatchMapsOptions(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}
	inspector := newRedisInspector(t)
	q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
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

	scheduled := waitForScheduledJob(t, inspector, queueName, 3*time.Second)
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
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}
	inspector := newRedisInspector(t)
	q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
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

	pending := waitForPendingJob(t, inspector, queueName, 3*time.Second)
	if pending.Timeout != redisDefaultJobTimeout {
		t.Fatalf("expected default timeout %s, got %s", redisDefaultJobTimeout, pending.Timeout)
	}
}

func TestRedisIntegration_UniqueDuplicateMapsToErrDuplicate(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}
	_ = newRedisInspector(t)
	q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
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
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}
	_ = newRedisInspector(t)
	q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}

	err = q.DispatchCtx(context.Background(), NewJob("job:backoff-unsupported").Payload([]byte("x")).OnQueue("default").Backoff(1*time.Second))
	if !errors.Is(err, ErrBackoffUnsupported) {
		t.Fatalf("expected ErrBackoffUnsupported, got %v", err)
	}
}

func TestRedisIntegration_BindPayloadThroughWorker(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}

	type payload struct {
		ID int `json:"id"`
	}
	received := make(chan payload, 1)

	q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}
	q.Register("job:bind", func(_ context.Context, job Job) error {
		var in payload
		if err := job.Bind(&in); err != nil {
			return err
		}
		received <- in
		return nil
	})
	if err := withWorkers(q, 1).StartWorkers(context.Background()); err != nil {
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

func waitForPendingJob(t *testing.T, inspector *asynq.Inspector, queueName string, timeout time.Duration) *asynq.TaskInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jobs, err := inspector.ListPendingTasks(queueName)
		if err != nil {
			t.Fatalf("list pending jobs failed: %v", err)
		}
		if len(jobs) > 0 {
			return jobs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending job not found for queue %q within %s", queueName, timeout)
	return nil
}

func waitForScheduledJob(t *testing.T, inspector *asynq.Inspector, queueName string, timeout time.Duration) *asynq.TaskInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jobs, err := inspector.ListScheduledTasks(queueName)
		if err != nil {
			t.Fatalf("list scheduled jobs failed: %v", err)
		}
		if len(jobs) > 0 {
			return jobs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("scheduled job not found for queue %q within %s", queueName, timeout)
	return nil
}

type scenarioFixture struct {
	name      string
	queueName string
	newQueue  func(t *testing.T) QueueRuntime
	newWorker func(t *testing.T) runtimeWorkerBackend

	supportsBackoff                  bool
	forceTimeout                     bool
	supportsRestart                  bool
	supportsRestartDelayedDurability bool
	supportsPoisonRetry              bool
	supportsDispatchCtxCancel        bool
	supportsDeterministicNoDupes     bool
	supportsOrderingContract         bool
	supportsBrokerFault              bool
	supportsShutdownDelayRetry       bool
}

type scenarioPayload struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestIntegrationScenarios_AllBackends(t *testing.T) {
	fixtures := []scenarioFixture{
		{
			name:      testenv.BackendRedis,
			queueName: "default",
			newQueue: func(t *testing.T) QueueRuntime {
				q, err := newQueueRuntime(redisCfg(integrationRedis.addr))
				if err != nil {
					t.Fatalf("new redis queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, redisCfg(integrationRedis.addr), 4)
			},
			supportsBackoff:                  false,
			forceTimeout:                     true,
			supportsRestart:                  true,
			supportsRestartDelayedDurability: true,
			supportsPoisonRetry:              false,
			supportsDispatchCtxCancel:        false,
			supportsDeterministicNoDupes:     true,
			supportsOrderingContract:         true,
			supportsBrokerFault:              true,
			supportsShutdownDelayRetry:       true,
		},
		{
			name:      testenv.BackendMySQL,
			queueName: "scenario_mysql",
			newQueue: func(t *testing.T) QueueRuntime {
				q, err := newQueueRuntime(mysqlCfg(mysqlDSN(integrationMySQL.addr)))
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, mysqlCfg(mysqlDSN(integrationMySQL.addr)), 4)
			},
			supportsBackoff:                  true,
			supportsRestart:                  true,
			supportsRestartDelayedDurability: true,
			supportsPoisonRetry:              true,
			supportsDispatchCtxCancel:        true,
			supportsDeterministicNoDupes:     true,
			supportsOrderingContract:         false,
			supportsBrokerFault:              false,
			supportsShutdownDelayRetry:       true,
		},
		{
			name:      testenv.BackendPostgres,
			queueName: "scenario_postgres",
			newQueue: func(t *testing.T) QueueRuntime {
				q, err := newQueueRuntime(postgresCfg(postgresDSN(integrationPostgres.addr)))
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, postgresCfg(postgresDSN(integrationPostgres.addr)), 4)
			},
			supportsBackoff:                  true,
			supportsRestart:                  true,
			supportsRestartDelayedDurability: true,
			supportsPoisonRetry:              true,
			supportsDispatchCtxCancel:        true,
			supportsDeterministicNoDupes:     true,
			supportsOrderingContract:         false,
			supportsBrokerFault:              false,
			supportsShutdownDelayRetry:       true,
		},
		{
			name:      testenv.BackendSQLite,
			queueName: "scenario_sqlite",
			newQueue: func(t *testing.T) QueueRuntime {
				q, err := newQueueRuntime(sqliteCfg(fmt.Sprintf("%s/scenario-%d.db", t.TempDir(), time.Now().UnixNano())))
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
			supportsBackoff:                  true,
			supportsRestart:                  false,
			supportsRestartDelayedDurability: false,
			supportsPoisonRetry:              true,
			supportsDispatchCtxCancel:        true,
			supportsDeterministicNoDupes:     true,
			supportsOrderingContract:         false,
			supportsBrokerFault:              false,
			supportsShutdownDelayRetry:       true,
		},
		{
			name:      testenv.BackendNATS,
			queueName: "scenario_nats",
			newQueue: func(t *testing.T) QueueRuntime {
				q, err := newQueueRuntime(natsCfg(integrationNATS.url))
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, natsCfg(integrationNATS.url), 4)
			},
			supportsBackoff:                  true,
			supportsRestart:                  false,
			supportsRestartDelayedDurability: false,
			supportsPoisonRetry:              true,
			supportsDispatchCtxCancel:        false,
			supportsDeterministicNoDupes:     false,
			supportsOrderingContract:         false,
			supportsBrokerFault:              false,
			supportsShutdownDelayRetry:       false,
		},
		{
			name:      testenv.BackendSQS,
			queueName: "scenario_sqs",
			newQueue: func(t *testing.T) QueueRuntime {
				q, err := newQueueRuntime(withDefaultQueue(
					sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey),
					"scenario_sqs",
				))
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, withDefaultQueue(
					sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey),
					"scenario_sqs",
				), 4)
			},
			supportsBackoff:                  true,
			supportsRestart:                  true,
			supportsRestartDelayedDurability: false,
			supportsPoisonRetry:              true,
			supportsDispatchCtxCancel:        true,
			supportsDeterministicNoDupes:     true,
			supportsOrderingContract:         false,
			supportsBrokerFault:              false,
			supportsShutdownDelayRetry:       false,
		},
		{
			name:      testenv.BackendRabbitMQ,
			queueName: "scenario_rabbitmq",
			newQueue: func(t *testing.T) QueueRuntime {
				q, err := newQueueRuntime(withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), "scenario_rabbitmq"))
				if err != nil {
					t.Fatalf("new rabbitmq queue failed: %v", err)
				}
				return q
			},
			newWorker: func(t *testing.T) runtimeWorkerBackend {
				return newQueueBackedWorker(t, withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), "scenario_rabbitmq"), 4)
			},
			supportsBackoff:                  true,
			supportsRestart:                  true,
			supportsRestartDelayedDurability: true,
			supportsPoisonRetry:              true,
			supportsDispatchCtxCancel:        false,
			supportsDeterministicNoDupes:     true,
			supportsOrderingContract:         false,
			supportsBrokerFault:              false,
			supportsShutdownDelayRetry:       true,
		},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if !integrationBackendEnabled(fx.name) {
				t.Skipf("%s integration backend not selected", fx.name)
			}
			t.Parallel()

			// SQLite needs a shared DSN between producer and worker; build it inline.
			if fx.name == testenv.BackendSQLite {
				dsn := fmt.Sprintf("%s/scenario-%d.db", t.TempDir(), time.Now().UnixNano())
				fx.newQueue = func(t *testing.T) QueueRuntime {
					q, err := newQueueRuntime(sqliteCfg(dsn))
					if err != nil {
						t.Fatalf("new sqlite queue failed: %v", err)
					}
					return q
				}
				fx.newWorker = func(t *testing.T) runtimeWorkerBackend {
					return newQueueBackedWorker(t, sqliteCfg(dsn), 4)
				}
				fx.supportsBackoff = true
				fx.supportsRestart = true
				fx.supportsRestartDelayedDurability = true
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
	jobTimeout := 250 * time.Millisecond
	if fx.forceTimeout {
		jobTimeout = 2 * time.Second
	}
	var seen atomic.Int32
	var expected atomic.Int32

	t.Run("scenario_register_handler", func(t *testing.T) {
		w.Register(jobType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
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

	t.Run("scenario_worker_ready_probe", func(t *testing.T) {
		probeType := "job:scenario:startup-probe:" + fx.name
		probeDone := make(chan struct{}, 1)
		w.Register(probeType, func(_ context.Context, _ Job) error {
			select {
			case probeDone <- struct{}{}:
			default:
			}
			return nil
		})

		probe := NewJob(probeType).
			Payload(scenarioPayload{ID: -1, Name: "startup-probe"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			probe = probe.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "worker_ready_probe_dispatch", q.DispatchCtx(context.Background(), probe))

		waitBudget := 5 * time.Second
		if fx.name == testenv.BackendNATS {
			waitBudget = 15 * time.Second
		}
		select {
		case <-probeDone:
		case <-time.After(waitBudget):
			t.Fatalf("[worker_ready_probe_processed] startup probe not processed within %s", waitBudget)
		}
	})

	t.Run("scenario_dispatch_burst", func(t *testing.T) {
		var wg sync.WaitGroup
		errCh := make(chan error, total)
		for i := 0; i < int(total); i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				job := NewJob(jobType).
					Payload(scenarioPayload{ID: i, Name: fx.name}).
					OnQueue(fx.queueName)
				if i%3 == 0 {
					job = job.Delay(50 * time.Millisecond)
				}
				if i%4 == 0 {
					job = job.Timeout(jobTimeout)
				}
				if fx.forceTimeout && i%4 != 0 {
					job = job.Timeout(jobTimeout)
				}
				if fx.supportsBackoff && i%5 == 0 {
					job = job.Retry(1).Backoff(20 * time.Millisecond)
				}
				if err := q.DispatchCtx(context.Background(), job); err != nil {
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
		waitBudget := 25 * time.Second
		if fx.name == testenv.BackendNATS {
			// NATS can run close to the deadline under full all-backends parallel CI load.
			waitBudget = 45 * time.Second
		}
		deadline := time.Now().Add(waitBudget)
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
			OnQueue(fx.queueName)
		poisonRetries := 2
		if fx.name == testenv.BackendRedis {
			// Redis/Asynq uses driver-managed retry delays when custom backoff is unsupported.
			// Use fewer retries here to keep the poison/recovery invariant fast in CI.
			poisonRetries = 1
		}
		poison = poison.Retry(poisonRetries)
		if fx.forceTimeout {
			poison = poison.Timeout(jobTimeout)
		}
		if fx.supportsBackoff {
			poison = poison.Backoff(20 * time.Millisecond)
		}
		requireScenarioNoErr(t, "poison_dispatch", q.DispatchCtx(context.Background(), poison))

		poisonWait := 10 * time.Second
		if fx.name == testenv.BackendMySQL || fx.name == testenv.BackendPostgres || fx.name == testenv.BackendSQLite {
			// DB-backed workers can need extra time to recover from transient driver/connection
			// errors in CI before retry attempts resume.
			poisonWait = 30 * time.Second
		}
		deadline := time.Now().Add(poisonWait)
		for time.Now().Before(deadline) {
			if poisonCalls.Load() >= 3 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		expectedPoisonCalls := int32(poisonRetries + 1)
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
		start := time.Now()
		restartQ := q
		restartW := w
		restartQueueName := fx.queueName
		if fx.name == testenv.BackendSQS {
			// SQS can retain duplicate deliveries from earlier scenarios long enough to
			// interfere with restart timing. Use an isolated physical queue for this
			// recovery invariant subtest so the result reflects restart behavior.
			restartQueueName = uniqueQueueName("scenario-sqs-restart-basic")
			restartCfg := withDefaultQueue(
				sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey),
				restartQueueName,
			)
			var err error
			restartQ, err = newQueueRuntime(restartCfg)
			if err != nil {
				t.Fatalf("[restart_basic_new_queue] %v", err)
			}
			restartW = newQueueBackedWorker(t, restartCfg, 4)
			t.Cleanup(func() { _ = restartQ.Shutdown(context.Background()) })
			t.Cleanup(func() { _ = restartW.Shutdown(context.Background()) })
		}
		requireScenarioNoErr(t, "restart_basic_worker_start", (restartW).StartWorkers(context.Background()))

		restartType := "job:scenario:restart-basic:" + fx.name
		done := make(chan struct{}, 1)

		restartW.Register(restartType, func(_ context.Context, _ Job) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})

		requireScenarioNoErr(t, "restart_basic_shutdown_first_worker", (restartW).Shutdown(context.Background()))
		job := NewJob(restartType).
			Payload(scenarioPayload{ID: 9099, Name: "restart-basic"}).
			OnQueue(restartQueueName)
		if fx.forceTimeout {
			job = job.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "restart_basic_dispatch_while_worker_down", restartQ.DispatchCtx(context.Background(), job))

		if fx.name == testenv.BackendSQS {
			restartCfg := withDefaultQueue(
				sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey),
				restartQueueName,
			)
			restartW = newQueueBackedWorker(t, restartCfg, 4)
			t.Cleanup(func() { _ = restartW.Shutdown(context.Background()) })
		} else {
			restartW = fx.newWorker(t)
			w = restartW
		}
		restartW.Register(restartType, func(_ context.Context, _ Job) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})
		requireScenarioNoErr(t, "restart_basic_start_second_worker", (restartW).StartWorkers(context.Background()))

		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Fatalf("[restart_basic_recovery_processing] queued job did not process after worker restart")
		}
		elapsed := time.Since(start)
		reportScenarioDuration(t, fx.name, "scenario_worker_restart_recovery", elapsed)
		limit := 20 * time.Second
		if fx.name == testenv.BackendSQS {
			limit = 30 * time.Second
		}
		requireScenarioDurationLTE(t, fx.name, "scenario_worker_restart_recovery", elapsed, limit)
	})

	t.Run("scenario_worker_restart_delay_recovery", func(t *testing.T) {
		if !fx.supportsRestartDelayedDurability {
			t.Skip("backend does not provide deterministic delayed/retry restart durability in this runtime")
		}
		start := time.Now()
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

		job := NewJob(restartType).
			Payload(scenarioPayload{ID: 9100, Name: "restart"}).
			OnQueue(fx.queueName).
			Delay(750 * time.Millisecond)
		if fx.forceTimeout {
			job = job.Timeout(jobTimeout)
		}
		if fx.supportsBackoff {
			job = job.Retry(1).Backoff(250 * time.Millisecond)
		}
		requireScenarioNoErr(t, "restart_dispatch", q.DispatchCtx(context.Background(), job))

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
		elapsed := time.Since(start)
		reportScenarioDuration(t, fx.name, "scenario_worker_restart_delay_recovery", elapsed)
		requireScenarioDurationLTE(t, fx.name, "scenario_worker_restart_delay_recovery", elapsed, 20*time.Second)
	})

	t.Run("scenario_bind_invalid_json", func(t *testing.T) {
		requireScenarioNoErr(t, "bind_scenario_worker_start", (w).StartWorkers(context.Background()))

		badType := "job:scenario:bind-bad:" + fx.name
		emptyType := "job:scenario:bind-empty:" + fx.name
		goodType := "job:scenario:bind-good:" + fx.name
		var badBindErrs atomic.Int32
		var emptyBindErrs atomic.Int32
		goodDone := make(chan struct{}, 1)

		w.Register(badType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
				badBindErrs.Add(1)
			}
			return nil
		})
		w.Register(emptyType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
				emptyBindErrs.Add(1)
			}
			return nil
		})
		w.Register(goodType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
				return err
			}
			select {
			case goodDone <- struct{}{}:
			default:
			}
			return nil
		})

		badJob := NewJob(badType).
			Payload([]byte("not-json")).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			badJob = badJob.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "bind_bad_dispatch", q.DispatchCtx(context.Background(), badJob))

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if badBindErrs.Load() >= 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "bind_bad_seen", badBindErrs.Load() == 1, "bind_errors=%d expected=1", badBindErrs.Load())

		emptyJob := NewJob(emptyType).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			emptyJob = emptyJob.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "bind_empty_dispatch", q.DispatchCtx(context.Background(), emptyJob))

		deadline = time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if emptyBindErrs.Load() >= 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "bind_empty_seen", emptyBindErrs.Load() == 1, "bind_errors=%d expected=1", emptyBindErrs.Load())

		goodJob := NewJob(goodType).
			Payload(scenarioPayload{ID: 9200, Name: "bind-good"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			goodJob = goodJob.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "bind_good_dispatch", q.DispatchCtx(context.Background(), goodJob))

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
			first = first.Timeout(jobTimeout)
			secondSameQueue = secondSameQueue.Timeout(jobTimeout)
			otherQueue = otherQueue.Timeout(jobTimeout)
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

		dispatchCanceledJob := func(t *testing.T, step string, ctx context.Context, payloadID int) error {
			t.Helper()
			job := NewJob(cancelType).
				Payload(scenarioPayload{ID: payloadID, Name: "ctx-cancel"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				job = job.Timeout(jobTimeout)
			}
			return q.DispatchCtx(ctx, job)
		}

		requireCancelBehavior := func(t *testing.T, step string, err error) {
			t.Helper()
			if !fx.supportsDispatchCtxCancel {
				return
			}
			requireScenarioTrue(
				t,
				step+"_err",
				errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded),
				"expected context cancellation error, got %v",
				err,
			)
			time.Sleep(250 * time.Millisecond)
			requireScenarioTrue(t, step+"_not_processed", cancelSeen.Load() == 0, "unexpected processed count=%d", cancelSeen.Load())
		}

		t.Run("scenario_dispatch_context_precanceled", func(t *testing.T) {
			cancelCtx, cancel := context.WithCancel(context.Background())
			cancel()
			err := dispatchCanceledJob(t, "dispatch_ctx_precanceled", cancelCtx, 9400)
			requireCancelBehavior(t, "dispatch_ctx_precanceled", err)
		})

		t.Run("scenario_dispatch_context_deadline_exceeded", func(t *testing.T) {
			deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
			defer cancel()
			err := dispatchCanceledJob(t, "dispatch_ctx_deadline", deadlineCtx, 9402)
			requireCancelBehavior(t, "dispatch_ctx_deadline", err)
		})

		t.Run("scenario_dispatch_context_followup_health", func(t *testing.T) {
			goodJob := NewJob(goodType).
				Payload(scenarioPayload{ID: 9401, Name: "ctx-good"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				goodJob = goodJob.Timeout(jobTimeout)
			}
			requireScenarioNoErr(t, "dispatch_ctx_good_dispatch", q.DispatchCtx(context.Background(), goodJob))
			select {
			case <-goodDone:
			case <-time.After(10 * time.Second):
				t.Fatalf("[dispatch_ctx_good_processed] follow-up job was not processed")
			}
		})
	})

	t.Run("scenario_shutdown_during_delay_retry", func(t *testing.T) {
		if !fx.supportsRestart || !fx.supportsShutdownDelayRetry {
			t.Skip("backend does not provide deterministic restart durability in this runtime")
		}
		start := time.Now()
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

		delayedJob := NewJob(delayedType).
			Payload(scenarioPayload{ID: 9500, Name: "shutdown-delay"}).
			OnQueue(fx.queueName).
			Delay(1200 * time.Millisecond)
		var retryJob Job
		retryEnabled := fx.supportsBackoff
		if retryEnabled {
			retryJob = NewJob(retryType).
				Payload(scenarioPayload{ID: 9501, Name: "shutdown-retry"}).
				OnQueue(fx.queueName).
				Delay(900 * time.Millisecond).
				Retry(1)
			retryJob = retryJob.Backoff(300 * time.Millisecond)
		}
		if fx.forceTimeout {
			delayedJob = delayedJob.Timeout(jobTimeout)
			if retryEnabled {
				retryJob = retryJob.Timeout(jobTimeout)
			}
		}
		requireScenarioNoErr(t, "shutdown_delay_dispatch", q.DispatchCtx(context.Background(), delayedJob))
		if retryEnabled {
			requireScenarioNoErr(t, "shutdown_retry_dispatch", q.DispatchCtx(context.Background(), retryJob))
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
		elapsed := time.Since(start)
		reportScenarioDuration(t, fx.name, "scenario_shutdown_during_delay_retry", elapsed)
		requireScenarioDurationLTE(t, fx.name, "scenario_shutdown_during_delay_retry", elapsed, 25*time.Second)
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
		const totalJobs = 30

		handler := func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
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

		for i := 0; i < totalJobs; i++ {
			job := NewJob(jobType).
				Payload(scenarioPayload{ID: i, Name: "multi-worker"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				job = job.Timeout(jobTimeout)
			}
			requireScenarioNoErr(t, "multi_worker_dispatch", q.DispatchCtx(context.Background(), job))
		}

		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if processed.Load() >= totalJobs {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireScenarioTrue(t, "multi_worker_processed_all", processed.Load() >= totalJobs, "processed=%d expected>=%d", processed.Load(), totalJobs)

		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		for i := 0; i < totalJobs; i++ {
			if seenByID[i] != 1 {
				t.Fatalf("[multi_worker_no_duplicate_success] job_id=%d count=%d expected=1", i, seenByID[i])
			}
		}
	})

	t.Run("scenario_duplicate_delivery_idempotency", func(t *testing.T) {
		start := time.Now()
		idempotencyQ := q
		idempotencyW := w
		idempotencyQueueName := fx.queueName
		if fx.name == testenv.BackendSQS {
			// SQS can retain invisible deliveries from earlier scenarios long enough to
			// delay duplicate-processing timing by the queue visibility timeout. Isolate
			// this subtest to a dedicated physical queue so it measures idempotency logic.
			idempotencyQueueName = uniqueQueueName("scenario-sqs-idempotency")
			idempotencyCfg := withDefaultQueue(
				sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey),
				idempotencyQueueName,
			)
			var err error
			idempotencyQ, err = newQueueRuntime(idempotencyCfg)
			if err != nil {
				t.Fatalf("[idempotency_new_queue] %v", err)
			}
			idempotencyW = newQueueBackedWorker(t, idempotencyCfg, 4)
			t.Cleanup(func() { _ = idempotencyQ.Shutdown(context.Background()) })
			t.Cleanup(func() { _ = idempotencyW.Shutdown(context.Background()) })
		}
		requireScenarioNoErr(t, "idempotency_worker_start", (idempotencyW).StartWorkers(context.Background()))

		jobType := "job:scenario:idempotency:" + fx.name
		var attempts atomic.Int32
		var committed atomic.Int32
		done := make(chan struct{}, 1)
		var mu sync.Mutex
		seen := make(map[int]struct{})

		idempotencyW.Register(jobType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
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
			OnQueue(idempotencyQueueName)
		second := NewJob(jobType).
			Payload(scenarioPayload{ID: 9600, Name: "idempotency"}).
			OnQueue(idempotencyQueueName)
		if fx.forceTimeout {
			first = first.Timeout(jobTimeout)
			second = second.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "idempotency_dispatch_first", idempotencyQ.DispatchCtx(context.Background(), first))
		requireScenarioNoErr(t, "idempotency_dispatch_second", idempotencyQ.DispatchCtx(context.Background(), second))

		select {
		case <-done:
		case <-time.After(45 * time.Second):
			t.Fatalf("[idempotency_done] duplicate deliveries did not both complete")
		}
		requireScenarioTrue(t, "idempotency_attempts", attempts.Load() >= 2, "attempts=%d expected>=2", attempts.Load())
		requireScenarioTrue(t, "idempotency_side_effect_once", committed.Load() == 1, "committed=%d expected=1", committed.Load())
		elapsed := time.Since(start)
		reportScenarioDuration(t, fx.name, "scenario_duplicate_delivery_idempotency", elapsed)
		if fx.name == testenv.BackendSQS {
			requireScenarioDurationLTE(t, fx.name, "scenario_duplicate_delivery_idempotency", elapsed, 45*time.Second)
		}
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
		job := NewJob("job:scenario:fault-dispatch:" + fx.name).
			Payload(scenarioPayload{ID: 9700, Name: "fault-dispatch"}).
			OnQueue(fx.queueName).
			Retry(1)
		if fx.forceTimeout {
			job = job.Timeout(jobTimeout)
		}
		err := qFault.DispatchCtx(badCtx, job)
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
		w.Register(jobType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
				return err
			}
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		})
		requireScenarioNoErr(t, "recover_worker_start", (w).StartWorkers(context.Background()))

		job := NewJob(jobType).
			Payload(scenarioPayload{ID: 9701, Name: "fault-recovery"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			job = job.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "recover_dispatch", q.DispatchCtx(context.Background(), job))
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Fatalf("[recover_processed] job was not processed after broker recovery")
		}
	})

	t.Run("scenario_ordering_contract", func(t *testing.T) {
		requireScenarioNoErr(t, "ordering_shutdown_previous_worker", (w).Shutdown(context.Background()))
		w = newOrderingWorker(t, fx)
		requireScenarioNoErr(t, "ordering_worker_start", (w).StartWorkers(context.Background()))

		collectOrderedIDs := func(t *testing.T, ch <-chan int, count int, timeout time.Duration) []int {
			t.Helper()
			got := make([]int, 0, count)
			deadline := time.After(timeout)
			for len(got) < count {
				select {
				case id := <-ch:
					got = append(got, id)
				case <-deadline:
					t.Fatalf("[ordering_collect] got=%d expected=%d", len(got), count)
				}
			}
			return got
		}
		orderingCollectTimeout := func(defaultTimeout time.Duration) time.Duration {
			// SQS/localstack can take significantly longer to drain small bursts in
			// some CI environments; treat this as a timing budget issue, not an
			// ordering semantic difference.
			if fx.name == testenv.BackendSQS && defaultTimeout < 60*time.Second {
				return 60 * time.Second
			}
			return defaultTimeout
		}

		t.Run("scenario_ordering_single_worker_fifo", func(t *testing.T) {
			if !fx.supportsOrderingContract {
				t.Skip("backend does not expose strict ordering guarantees in this suite")
			}
			jobType := "job:scenario:ordering:fifo:" + fx.name
			const count = 20
			orderCh := make(chan int, count)
			w.Register(jobType, func(_ context.Context, job Job) error {
				var payload scenarioPayload
				if err := job.Bind(&payload); err != nil {
					return err
				}
				orderCh <- payload.ID
				return nil
			})

			for i := 0; i < count; i++ {
				job := NewJob(jobType).
					Payload(scenarioPayload{ID: i, Name: "ordering-fifo"}).
					OnQueue(fx.queueName)
				if fx.forceTimeout {
					job = job.Timeout(jobTimeout)
				}
				requireScenarioNoErr(t, "ordering_fifo_dispatch", q.DispatchCtx(context.Background(), job))
			}

			got := collectOrderedIDs(t, orderCh, count, orderingCollectTimeout(15*time.Second))
			for i := 0; i < count; i++ {
				if got[i] != i {
					t.Fatalf("[ordering_fifo] index=%d got=%d expected=%d", i, got[i], i)
				}
			}
		})

		t.Run("scenario_ordering_delayed_immediate_mix", func(t *testing.T) {
			if !fx.supportsShutdownDelayRetry {
				t.Skip("backend does not provide stable delayed-work semantics for this ordering probe")
			}
			jobType := "job:scenario:ordering:delaymix:" + fx.name
			const count = 5
			orderCh := make(chan int, count)
			w.Register(jobType, func(_ context.Context, job Job) error {
				var payload scenarioPayload
				if err := job.Bind(&payload); err != nil {
					return err
				}
				orderCh <- payload.ID
				return nil
			})

			delayed := NewJob(jobType).
				Payload(scenarioPayload{ID: 0, Name: "ordering-delay"}).
				OnQueue(fx.queueName).
				Delay(800 * time.Millisecond)
			if fx.forceTimeout {
				delayed = delayed.Timeout(jobTimeout)
			}
			requireScenarioNoErr(t, "ordering_delaymix_dispatch_delayed", q.DispatchCtx(context.Background(), delayed))

			for i := 1; i < count; i++ {
				job := NewJob(jobType).
					Payload(scenarioPayload{ID: i, Name: "ordering-delay"}).
					OnQueue(fx.queueName)
				if fx.forceTimeout {
					job = job.Timeout(jobTimeout)
				}
				requireScenarioNoErr(t, "ordering_delaymix_dispatch_immediate", q.DispatchCtx(context.Background(), job))
			}

			got := collectOrderedIDs(t, orderCh, count, orderingCollectTimeout(20*time.Second))
			firstDelayedIdx := -1
			for i, id := range got {
				if id == 0 {
					firstDelayedIdx = i
					break
				}
			}
			if firstDelayedIdx <= 0 && fx.supportsOrderingContract {
				t.Fatalf("[ordering_delaymix_expected_reorder] delayed job executed too early; got=%v", got)
			}
			if firstDelayedIdx <= 0 && !fx.supportsOrderingContract {
				t.Logf("[%s][scenario_ordering_delayed_immediate_mix] delayed job executed first; no ordering guarantee is claimed", fx.name)
			}
		})

		t.Run("scenario_ordering_multi_worker_best_effort", func(t *testing.T) {
			if !(fx.supportsDeterministicNoDupes && fx.supportsShutdownDelayRetry) {
				t.Skip("backend does not provide stable multi-worker completion semantics for this ordering probe")
			}
			// This is a non-guarantee scenario: with concurrent workers we only
			// assert completion/correctness, not FIFO ordering.
			requireScenarioNoErr(t, "ordering_multi_shutdown_single_worker", (w).Shutdown(context.Background()))
			w = fx.newWorker(t)
			requireScenarioNoErr(t, "ordering_multi_worker_start", (w).StartWorkers(context.Background()))

			jobType := "job:scenario:ordering:multi:" + fx.name
			const count = 24
			orderCh := make(chan int, count)
			w.Register(jobType, func(_ context.Context, job Job) error {
				var payload scenarioPayload
				if err := job.Bind(&payload); err != nil {
					return err
				}
				// Small per-job variance increases interleaving likelihood without
				// making the scenario depend on a specific reorder outcome.
				time.Sleep(time.Duration((payload.ID%4)+1) * 5 * time.Millisecond)
				orderCh <- payload.ID
				return nil
			})

			for i := 0; i < count; i++ {
				job := NewJob(jobType).
					Payload(scenarioPayload{ID: i, Name: "ordering-multi"}).
					OnQueue(fx.queueName)
				if fx.forceTimeout {
					job = job.Timeout(jobTimeout)
				}
				requireScenarioNoErr(t, "ordering_multi_dispatch", q.DispatchCtx(context.Background(), job))
			}

			got := collectOrderedIDs(t, orderCh, count, orderingCollectTimeout(20*time.Second))
			seen := make(map[int]int, count)
			for _, id := range got {
				seen[id]++
			}
			for i := 0; i < count; i++ {
				if seen[i] != 1 {
					t.Fatalf("[ordering_multi_completeness] id=%d seen=%d got=%v", i, seen[i], got)
				}
			}
			isFIFO := true
			for i := 0; i < count; i++ {
				if got[i] != i {
					isFIFO = false
					break
				}
			}
			if isFIFO {
				t.Logf("[%s][scenario_ordering_multi_worker_best_effort] observed FIFO; no FIFO guarantee is claimed", fx.name)
			}
		})

		t.Run("scenario_ordering_retry_reorder_allowed", func(t *testing.T) {
			if !(fx.supportsBackoff && fx.supportsPoisonRetry && fx.supportsShutdownDelayRetry) {
				t.Skip("backend does not support deterministic retry reorder scenario in this suite")
			}
			jobType := "job:scenario:ordering:retry:" + fx.name
			const count = 4
			successCh := make(chan int, count)
			var attempts sync.Map

			w.Register(jobType, func(_ context.Context, job Job) error {
				var payload scenarioPayload
				if err := job.Bind(&payload); err != nil {
					return err
				}
				if payload.ID == 0 {
					var n int32 = 1
					if v, ok := attempts.Load(payload.ID); ok {
						n = atomic.AddInt32(v.(*int32), 1)
					} else {
						ptr := new(int32)
						*ptr = 1
						actual, loaded := attempts.LoadOrStore(payload.ID, ptr)
						if loaded {
							n = atomic.AddInt32(actual.(*int32), 1)
						}
					}
					if n == 1 {
						return errors.New("retry ordering probe")
					}
				}
				successCh <- payload.ID
				return nil
			})

			first := NewJob(jobType).
				Payload(scenarioPayload{ID: 0, Name: "ordering-retry"}).
				OnQueue(fx.queueName).
				Retry(1).
				Backoff(800 * time.Millisecond)
			if fx.forceTimeout {
				first = first.Timeout(jobTimeout)
			}
			requireScenarioNoErr(t, "ordering_retry_dispatch_first", q.DispatchCtx(context.Background(), first))

			for i := 1; i < count; i++ {
				job := NewJob(jobType).
					Payload(scenarioPayload{ID: i, Name: "ordering-retry"}).
					OnQueue(fx.queueName)
				if fx.forceTimeout {
					job = job.Timeout(jobTimeout)
				}
				requireScenarioNoErr(t, "ordering_retry_dispatch_followup", q.DispatchCtx(context.Background(), job))
			}

			got := collectOrderedIDs(t, successCh, count, orderingCollectTimeout(25*time.Second))
			retrySuccessIdx := -1
			for i, id := range got {
				if id == 0 {
					retrySuccessIdx = i
					break
				}
			}
			if retrySuccessIdx <= 0 {
				t.Fatalf("[ordering_retry_expected_reorder] retried job did not move behind immediate work; got=%v", got)
			}
		})
	})

	t.Run("scenario_retry_delay_timing_windows", func(t *testing.T) {
		requireScenarioNoErr(t, "timing_worker_start", (w).StartWorkers(context.Background()))

		t.Run("scenario_delay_not_before_window", func(t *testing.T) {
			if !fx.supportsShutdownDelayRetry {
				t.Skip("backend does not provide stable delayed-work timing semantics in this suite")
			}
			jobType := "job:scenario:timing:delay:" + fx.name
			startedCh := make(chan time.Time, 1)
			w.Register(jobType, func(_ context.Context, job Job) error {
				var payload scenarioPayload
				if err := job.Bind(&payload); err != nil {
					return err
				}
				select {
				case startedCh <- time.Now():
				default:
				}
				return nil
			})

			const delay = 1200 * time.Millisecond
			const earlyTolerance = 250 * time.Millisecond

			dispatchAt := time.Now()
			job := NewJob(jobType).
				Payload(scenarioPayload{ID: 9900, Name: "timing-delay"}).
				OnQueue(fx.queueName).
				Delay(delay)
			if fx.forceTimeout {
				job = job.Timeout(jobTimeout)
			}
			requireScenarioNoErr(t, "timing_delay_dispatch", q.DispatchCtx(context.Background(), job))

			var startedAt time.Time
			select {
			case startedAt = <-startedCh:
			case <-time.After(20 * time.Second):
				t.Fatalf("[timing_delay_collect] delayed job did not execute")
			}

			elapsed := startedAt.Sub(dispatchAt)
			reportScenarioDuration(t, fx.name, "scenario_delay_not_before_window", elapsed)
			minAllowed := delay - earlyTolerance
			if elapsed < minAllowed {
				t.Fatalf("[timing_delay_early] elapsed=%s min_allowed=%s delay=%s tolerance=%s",
					elapsed.Round(time.Millisecond), minAllowed, delay, earlyTolerance)
			}
		})

		t.Run("scenario_retry_backoff_not_before_window", func(t *testing.T) {
			if !(fx.supportsBackoff && fx.supportsPoisonRetry && fx.supportsShutdownDelayRetry) {
				t.Skip("backend does not support deterministic custom backoff retry timing in this suite")
			}

			jobType := "job:scenario:timing:retry:" + fx.name
			attemptTimes := make(chan time.Time, 2)
			var attempts atomic.Int32
			w.Register(jobType, func(_ context.Context, job Job) error {
				var payload scenarioPayload
				if err := job.Bind(&payload); err != nil {
					return err
				}
				attempt := attempts.Add(1)
				select {
				case attemptTimes <- time.Now():
				default:
				}
				if attempt == 1 {
					return errors.New("retry timing probe")
				}
				return nil
			})

			const backoff = 1200 * time.Millisecond
			const earlyTolerance = 300 * time.Millisecond
			job := NewJob(jobType).
				Payload(scenarioPayload{ID: 9901, Name: "timing-retry"}).
				OnQueue(fx.queueName).
				Retry(1).
				Backoff(backoff)
			if fx.forceTimeout {
				job = job.Timeout(jobTimeout)
			}
			requireScenarioNoErr(t, "timing_retry_dispatch", q.DispatchCtx(context.Background(), job))

			var firstAt, secondAt time.Time
			select {
			case firstAt = <-attemptTimes:
			case <-time.After(15 * time.Second):
				t.Fatalf("[timing_retry_first_attempt] first attempt did not execute")
			}
			select {
			case secondAt = <-attemptTimes:
			case <-time.After(25 * time.Second):
				t.Fatalf("[timing_retry_second_attempt] second attempt did not execute")
			}

			elapsed := secondAt.Sub(firstAt)
			reportScenarioDuration(t, fx.name, "scenario_retry_backoff_not_before_window", elapsed)
			minAllowed := backoff - earlyTolerance
			if elapsed < minAllowed {
				t.Fatalf("[timing_retry_early] elapsed=%s min_allowed=%s backoff=%s tolerance=%s",
					elapsed.Round(time.Millisecond), minAllowed, backoff, earlyTolerance)
			}
		})
	})

	t.Run("scenario_backpressure_saturation", func(t *testing.T) {
		jobType := "job:scenario:backpressure:" + fx.name
		var processed atomic.Int32
		w.Register(jobType, func(_ context.Context, _ Job) error {
			time.Sleep(40 * time.Millisecond)
			processed.Add(1)
			return nil
		})

		// Non-durable backends (for example NATS) can drop messages published
		// before workers subscribe. Start workers first for those backends.
		if !fx.supportsRestartDelayedDurability {
			requireScenarioNoErr(t, "backpressure_worker_start", (w).StartWorkers(context.Background()))
		}

		const total = 80
		for i := 0; i < total; i++ {
			job := NewJob(jobType).
				Payload(scenarioPayload{ID: i, Name: "backpressure"}).
				OnQueue(fx.queueName)
			if fx.forceTimeout {
				job = job.Timeout(jobTimeout)
			}
			requireScenarioNoErr(t, "backpressure_dispatch", q.DispatchCtx(context.Background(), job))
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
		// For durable backends, start after dispatch to maximize initial saturation.
		if fx.supportsRestartDelayedDurability {
			// Register both handlers before starting workers so this scenario measures
			// saturation behavior rather than dynamic handler/subscription propagation.
			requireScenarioNoErr(t, "backpressure_worker_start", (w).StartWorkers(context.Background()))
		}

		// Prove workers are actively draining the saturated queue before publishing the probe.
		progressDeadline := time.After(20 * time.Second)
		progressTicker := time.NewTicker(50 * time.Millisecond)
		defer progressTicker.Stop()
		progressSeen := false
		for !progressSeen {
			if processed.Load() > 0 {
				progressSeen = true
				break
			}
			select {
			case <-progressDeadline:
				t.Fatalf("[backpressure_progress_before_probe] processed=%d expected>0", processed.Load())
			case <-progressTicker.C:
			}
		}

		probe := NewJob(probeType).
			Payload(scenarioPayload{ID: 9800, Name: "probe"}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			probe = probe.Timeout(jobTimeout)
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
		w.Register(jobType, func(_ context.Context, job Job) error {
			var payload struct {
				ID   int    `json:"id"`
				Data string `json:"data"`
			}
			if err := job.Bind(&payload); err != nil {
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

		job := NewJob(jobType).
			Payload(struct {
				ID   int    `json:"id"`
				Data string `json:"data"`
			}{ID: 9900, Data: strings.Repeat("x", expectedLen)}).
			OnQueue(fx.queueName)
		if fx.forceTimeout {
			job = job.Timeout(jobTimeout)
		}
		requireScenarioNoErr(t, "payload_large_dispatch", q.DispatchCtx(context.Background(), job))
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
		w.Register(jobType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
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
			job := NewJob(jobType).
				Payload(scenarioPayload{ID: 10000 + i, Name: "fuzz"}).
				OnQueue(fx.queueName)
			if r.Intn(2) == 0 {
				job = job.Delay(time.Duration(20+r.Intn(120)) * time.Millisecond)
			}
			if r.Intn(2) == 0 {
				timeout := time.Duration(80+r.Intn(260)) * time.Millisecond
				if fx.forceTimeout && timeout < time.Second {
					timeout = 2 * time.Second
				}
				job = job.Timeout(timeout)
			}
			retrySet := false
			if r.Intn(2) == 0 {
				job = job.Retry(r.Intn(3))
				retrySet = true
			}
			if retrySet && fx.supportsBackoff && r.Intn(2) == 0 {
				job = job.Backoff(time.Duration(30+r.Intn(160)) * time.Millisecond)
			}
			if r.Intn(4) == 0 {
				job = job.UniqueFor(time.Duration(1+r.Intn(2)) * time.Second)
			}
			if err := q.DispatchCtx(context.Background(), job); err != nil {
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
		w.Register(jobType, func(_ context.Context, job Job) error {
			var payload scenarioPayload
			if err := job.Bind(&payload); err != nil {
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
					job := NewJob(jobType).
						Payload(scenarioPayload{ID: id, Name: "soak"}).
						OnQueue(fx.queueName)
					if id%5 == 0 {
						job = job.Delay(30 * time.Millisecond)
					}
					if id%6 == 0 {
						timeout := 250 * time.Millisecond
						if fx.forceTimeout {
							timeout = 2 * time.Second
						}
						job = job.Timeout(timeout)
					}
					if id%8 == 0 {
						job = job.Retry(1)
						if fx.supportsBackoff {
							job = job.Backoff(80 * time.Millisecond)
						}
					}
					if id%10 == 0 {
						job = job.UniqueFor(1 * time.Second)
					}
					if err := q.DispatchCtx(context.Background(), job); err != nil {
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
	q       QueueRuntime
	workers int
}

func newQueueBackedWorker(t *testing.T, cfg any, workers int) runtimeWorkerBackend {
	t.Helper()
	q, err := newQueueRuntime(cfg)
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
	return withWorkers(w.q, w.workers).StartWorkers(ctx)
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
	case testenv.BackendRedis:
		return newQueueBackedWorker(t, redisCfg(integrationRedis.addr), 1)
	case testenv.BackendMySQL:
		return newQueueBackedWorker(t, mysqlCfg(mysqlDSN(integrationMySQL.addr)), 1)
	case testenv.BackendPostgres:
		return newQueueBackedWorker(t, postgresCfg(postgresDSN(integrationPostgres.addr)), 1)
	default:
		return fx.newWorker(t)
	}
}
