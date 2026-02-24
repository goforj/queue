//go:build integration

package root_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var integrationRabbitMQ struct {
	container testcontainers.Container
	url       string
}

func ensureRabbitMQ(t testing.TB) string {
	t.Helper()
	if integrationRabbitMQ.url != "" {
		return integrationRabbitMQ.url
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req := testcontainers.ContainerRequest{
		Image:        "rabbitmq:3.13-management",
		ExposedPorts: []string{"5672/tcp"},
		WaitingFor:   wait.ForListeningPort("5672/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start rabbitmq container: %v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("rabbitmq host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5672/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("rabbitmq port: %v", err)
	}
	integrationRabbitMQ.container = container
	integrationRabbitMQ.url = "amqp://guest:guest@" + net.JoinHostPort(host, port.Port()) + "/"
	return integrationRabbitMQ.url
}

func uniqueQueueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

type scenarioPayload struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func newRabbitMQIntegrationConfig(t *testing.T) any {
	return rabbitmqCfg(ensureRabbitMQ(t))
}

func newRabbitMQIntegrationConfigForQueue(t *testing.T, queueName string) any {
	return withDefaultQueue(rabbitmqCfg(ensureRabbitMQ(t)), queueName)
}

func TestRabbitMQIntegration_BindPayloadThroughWorker(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	type payload struct {
		ID int `json:"id"`
	}
	received := make(chan payload, 1)

	q, err := newQueueRuntime(newRabbitMQIntegrationConfig(t))
	if err != nil {
		t.Fatalf("new rabbitmq queue failed: %v", err)
	}
	q.Register("job:rabbitmq:bind", func(_ context.Context, job queue.Job) error {
		var in payload
		if err := job.Bind(&in); err != nil {
			return err
		}
		received <- in
		return nil
	})
	if err := withWorkers(q, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq queue start failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	want := payload{ID: 42}
	if err := q.DispatchCtx(context.Background(), queue.NewJob("job:rabbitmq:bind").Payload(want).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case got := <-received:
		if got != want {
			t.Fatalf("bind payload mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for rabbitmq worker bind payload")
	}
}

func TestRabbitMQIntegration_OptionBehavior(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	delay := 250 * time.Millisecond
	backoff := 200 * time.Millisecond
	timeout := 120 * time.Millisecond
	started := time.Now()
	done := make(chan struct{}, 1)
	var calls atomic.Int32
	deadlineSeen := make(chan bool, 1)

	q, err := newQueueRuntime(newRabbitMQIntegrationConfig(t))
	if err != nil {
		t.Fatalf("new rabbitmq queue failed: %v", err)
	}
	q.Register("job:rabbitmq:opts", func(ctx context.Context, _ queue.Job) error {
		if calls.Add(1) == 1 {
			_, ok := ctx.Deadline()
			deadlineSeen <- ok
		}
		if calls.Load() < 3 {
			return errors.New("retry-me")
		}
		done <- struct{}{}
		return nil
	})
	if err := withWorkers(q, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq queue start failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	job := queue.NewJob("job:rabbitmq:opts").
		Payload([]byte("opts")).
		OnQueue("default").
		Delay(delay).
		Timeout(timeout).
		Retry(2).
		Backoff(backoff)
	if err := q.DispatchCtx(context.Background(), job); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case ok := <-deadlineSeen:
		if !ok {
			t.Fatal("expected handler context deadline for timeout option")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first rabbitmq handler attempt")
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for rabbitmq retry flow")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
	elapsed := time.Since(started)
	if elapsed < delay+2*backoff-100*time.Millisecond {
		t.Fatalf("expected elapsed to include delay and backoff, got %s", elapsed)
	}
}

func TestRabbitMQIntegration_UniqueDuplicate(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	q, err := newQueueRuntime(newRabbitMQIntegrationConfig(t))
	if err != nil {
		t.Fatalf("new rabbitmq queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	jobType := "job:rabbitmq:unique"
	payload := []byte("same")
	first := queue.NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(500 * time.Millisecond)
	if err := q.DispatchCtx(context.Background(), first); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	second := queue.NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(500 * time.Millisecond)
	err = q.DispatchCtx(context.Background(), second)
	if !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if err := q.DispatchCtx(context.Background(), second); err != nil {
		t.Fatalf("dispatch after ttl failed: %v", err)
	}
}

func TestRabbitMQIntegration_RoutesToJobQueue(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	queueName := uniqueQueueName("rabbitmq-route")
	jobType := "job:rabbitmq:route"
	done := make(chan struct{}, 1)

	consumer, err := newQueueRuntime(newRabbitMQIntegrationConfigForQueue(t, queueName))
	if err != nil {
		t.Fatalf("new rabbitmq consumer queue failed: %v", err)
	}
	consumer.Register(jobType, func(_ context.Context, _ queue.Job) error {
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})
	if err := withWorkers(consumer, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue start failed: %v", err)
	}
	defer consumer.Shutdown(context.Background())

	producer, err := newQueueRuntime(newRabbitMQIntegrationConfig(t))
	if err != nil {
		t.Fatalf("new rabbitmq producer queue failed: %v", err)
	}
	defer producer.Shutdown(context.Background())

	if err := producer.DispatchCtx(context.Background(), queue.NewJob(jobType).OnQueue(queueName)); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for routed rabbitmq job")
	}
}

func TestRabbitMQIntegration_DelaySurvivesWorkerRestart(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	queueName := uniqueQueueName("rabbitmq-delay-restart")
	jobType := "job:rabbitmq:delay-restart"
	delay := 2 * time.Second
	start := time.Now()
	done := make(chan struct{}, 1)

	consumer1, err := newQueueRuntime(newRabbitMQIntegrationConfigForQueue(t, queueName))
	if err != nil {
		t.Fatalf("new rabbitmq consumer queue failed: %v", err)
	}
	consumer1.Register(jobType, func(_ context.Context, _ queue.Job) error {
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})
	if err := withWorkers(consumer1, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue start failed: %v", err)
	}

	producer, err := newQueueRuntime(newRabbitMQIntegrationConfig(t))
	if err != nil {
		t.Fatalf("new rabbitmq queue failed: %v", err)
	}
	defer producer.Shutdown(context.Background())

	job := queue.NewJob(jobType).
		Payload(scenarioPayload{ID: 1, Name: "delay-restart"}).
		OnQueue(queueName).
		Delay(delay)
	if err := producer.DispatchCtx(context.Background(), job); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if err := consumer1.Shutdown(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue shutdown failed: %v", err)
	}

	consumer2, err := newQueueRuntime(newRabbitMQIntegrationConfigForQueue(t, queueName))
	if err != nil {
		t.Fatalf("new rabbitmq consumer queue 2 failed: %v", err)
	}
	consumer2.Register(jobType, func(_ context.Context, _ queue.Job) error {
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})
	if err := withWorkers(consumer2, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue 2 start failed: %v", err)
	}
	defer consumer2.Shutdown(context.Background())

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for delayed job after worker restart")
	}
	if elapsed := time.Since(start); elapsed < delay-150*time.Millisecond {
		t.Fatalf("expected delayed execution after about %s, got %s", delay, elapsed)
	}
}

func TestRabbitMQIntegration_RetryBackoffSurvivesWorkerRestart(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	queueName := uniqueQueueName("rabbitmq-retry-restart")
	jobType := "job:rabbitmq:retry-restart"
	backoff := 1200 * time.Millisecond
	firstAttempt := make(chan struct{}, 1)
	done := make(chan struct{}, 1)
	var calls atomic.Int32

	consumer1, err := newQueueRuntime(newRabbitMQIntegrationConfigForQueue(t, queueName))
	if err != nil {
		t.Fatalf("new rabbitmq consumer queue failed: %v", err)
	}
	consumer1.Register(jobType, func(_ context.Context, _ queue.Job) error {
		if calls.Add(1) == 1 {
			select {
			case firstAttempt <- struct{}{}:
			default:
			}
			return errors.New("retry once")
		}
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})
	if err := withWorkers(consumer1, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue start failed: %v", err)
	}

	producer, err := newQueueRuntime(newRabbitMQIntegrationConfig(t))
	if err != nil {
		t.Fatalf("new rabbitmq queue failed: %v", err)
	}
	defer producer.Shutdown(context.Background())

	job := queue.NewJob(jobType).
		Payload(scenarioPayload{ID: 2, Name: "retry-restart"}).
		OnQueue(queueName).
		Retry(1).
		Backoff(backoff)
	if err := producer.DispatchCtx(context.Background(), job); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-firstAttempt:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first retry attempt")
	}
	if err := consumer1.Shutdown(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue shutdown failed: %v", err)
	}

	consumer2, err := newQueueRuntime(newRabbitMQIntegrationConfigForQueue(t, queueName))
	if err != nil {
		t.Fatalf("new rabbitmq consumer queue 2 failed: %v", err)
	}
	consumer2.Register(jobType, func(_ context.Context, _ queue.Job) error {
		if calls.Add(1) == 1 {
			return errors.New("retry once")
		}
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})
	if err := withWorkers(consumer2, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue 2 start failed: %v", err)
	}
	defer consumer2.Shutdown(context.Background())

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for retry completion after worker restart")
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", got)
	}
}

func TestRabbitMQIntegration_DelayQueueBehavior(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	queueName := uniqueQueueName("rabbitmq-delay-queue")
	jobType := "job:rabbitmq:delay-queue"
	delay := 2 * time.Second
	start := time.Now()
	done := make(chan struct{}, 1)

	consumer, err := newQueueRuntime(newRabbitMQIntegrationConfigForQueue(t, queueName))
	if err != nil {
		t.Fatalf("new rabbitmq consumer queue failed: %v", err)
	}
	consumer.Register(jobType, func(_ context.Context, _ queue.Job) error {
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})
	if err := withWorkers(consumer, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("rabbitmq consumer queue start failed: %v", err)
	}
	defer consumer.Shutdown(context.Background())

	q, err := newQueueRuntime(newRabbitMQIntegrationConfig(t))
	if err != nil {
		t.Fatalf("new rabbitmq queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	job := queue.NewJob(jobType).
		Payload(scenarioPayload{ID: 3, Name: "delay-queue"}).
		OnQueue(queueName).
		Delay(delay)
	if err := q.DispatchCtx(context.Background(), job); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	conn, err := amqp.Dial(integrationRabbitMQ.url)
	if err != nil {
		t.Fatalf("dial rabbitmq failed: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open rabbitmq channel failed: %v", err)
	}
	defer ch.Close()

	delayQueue := fmt.Sprintf("%s.delay", queueName)
	deadline := time.Now().Add(5 * time.Second)
	sawBuffered := false
	for time.Now().Before(deadline) {
		info, inspectErr := ch.QueueInspect(delayQueue)
		if inspectErr == nil && info.Messages > 0 {
			sawBuffered = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawBuffered {
		t.Fatalf("expected delay queue %q to contain buffered messages", delayQueue)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for delayed job processing")
	}
	if elapsed := time.Since(start); elapsed < delay-150*time.Millisecond {
		t.Fatalf("expected delayed execution after about %s, got %s", delay, elapsed)
	}
}
