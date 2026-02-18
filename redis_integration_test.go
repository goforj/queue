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

func TestMain(m *testing.M) {
	ctx := context.Background()
	backends := selectedIntegrationBackends()
	needsRedis := backends["redis"]
	needsMySQL := backends["mysql"]
	needsPostgres := backends["postgres"]

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

	os.Exit(exitCode)
}

func selectedIntegrationBackends() map[string]bool {
	selected := map[string]bool{
		"redis":    true,
		"mysql":    true,
		"postgres": true,
		"sqlite":   true,
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
