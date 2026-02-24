//go:build integration

package root_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var integrationSQS struct {
	container testcontainers.Container
	endpoint  string
	region    string
	accessKey string
	secretKey string
}

func ensureSQS(t testing.TB) {
	t.Helper()
	if integrationSQS.endpoint != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
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
		t.Fatalf("start sqs container: %v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("sqs host: %v", err)
	}
	port, err := container.MappedPort(ctx, "4566/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("sqs port: %v", err)
	}
	integrationSQS.container = container
	integrationSQS.endpoint = "http://" + net.JoinHostPort(host, port.Port())
	integrationSQS.region = "us-east-1"
	integrationSQS.accessKey = "test"
	integrationSQS.secretKey = "test"
}

func newSQSIntegrationConfig(t *testing.T, defaultQueue string) queue.Config {
	ensureSQS(t)
	return queue.Config{
		Driver:       queue.DriverSQS,
		DefaultQueue: defaultQueue,
		SQSEndpoint:  integrationSQS.endpoint,
		SQSRegion:    integrationSQS.region,
		SQSAccessKey: integrationSQS.accessKey,
		SQSSecretKey: integrationSQS.secretKey,
	}
}

func TestSQSIntegration_BindPayloadThroughWorker(t *testing.T) {
	if !integrationBackendEnabled("sqs") {
		t.Skip("sqs integration backend not selected")
	}
	type payload struct {
		ID int `json:"id"`
	}
	received := make(chan payload, 1)
	queueName := uniqueQueueName("sqs-bind")

	q, err := newQueueRuntime(newSQSIntegrationConfig(t, queueName))
	if err != nil {
		t.Fatalf("new sqs queue failed: %v", err)
	}
	q.Register("job:sqs:bind", func(_ context.Context, job queue.Job) error {
		var in payload
		if err := job.Bind(&in); err != nil {
			return err
		}
		received <- in
		return nil
	})
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("sqs queue start failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	want := payload{ID: 42}
	if err := q.DispatchCtx(context.Background(), queue.NewJob("job:sqs:bind").Payload(want).OnQueue(queueName)); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case got := <-received:
		if got != want {
			t.Fatalf("bind payload mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for sqs worker bind payload")
	}
}

func TestSQSIntegration_OptionBehavior(t *testing.T) {
	if !integrationBackendEnabled("sqs") {
		t.Skip("sqs integration backend not selected")
	}
	delay := 1200 * time.Millisecond
	backoff := 1100 * time.Millisecond
	timeout := 100 * time.Millisecond
	started := time.Now()
	done := make(chan struct{}, 1)
	var calls atomic.Int32
	deadlineSeen := make(chan bool, 1)
	queueName := uniqueQueueName("sqs-opts")

	q, err := newQueueRuntime(newSQSIntegrationConfig(t, queueName))
	if err != nil {
		t.Fatalf("new sqs queue failed: %v", err)
	}
	q.Register("job:sqs:opts", func(ctx context.Context, _ queue.Job) error {
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
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("sqs queue start failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	job := queue.NewJob("job:sqs:opts").
		Payload([]byte("opts")).
		OnQueue(queueName).
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
		t.Fatal("timed out waiting for first sqs handler attempt")
	}
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("timed out waiting for sqs retry flow")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
	elapsed := time.Since(started)
	if elapsed < delay+2*backoff-400*time.Millisecond {
		t.Fatalf("expected elapsed to include delay and backoff, got %s", elapsed)
	}
}

func TestSQSIntegration_UniqueDuplicate(t *testing.T) {
	if !integrationBackendEnabled("sqs") {
		t.Skip("sqs integration backend not selected")
	}
	queueName := uniqueQueueName("sqs-unique")
	q, err := newQueueRuntime(newSQSIntegrationConfig(t, queueName))
	if err != nil {
		t.Fatalf("new sqs queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	jobType := "job:sqs:unique"
	payload := []byte("same")
	first := queue.NewJob(jobType).Payload(payload).OnQueue(queueName).UniqueFor(500 * time.Millisecond)
	if err := q.DispatchCtx(context.Background(), first); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	second := queue.NewJob(jobType).Payload(payload).OnQueue(queueName).UniqueFor(500 * time.Millisecond)
	err = q.DispatchCtx(context.Background(), second)
	if !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if err := q.DispatchCtx(context.Background(), second); err != nil {
		t.Fatalf("dispatch after ttl failed: %v", err)
	}
}
