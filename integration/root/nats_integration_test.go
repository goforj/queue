//go:build integration

package root_test

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var integrationNATS struct {
	container testcontainers.Container
	url       string
}

func integrationBackendEnabled(name string) bool {
	return testenv.BackendEnabled(os.Getenv("INTEGRATION_BACKEND"), name)
}

func ensureNATS(t testing.TB) string {
	t.Helper()
	if integrationNATS.url != "" {
		return integrationNATS.url
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
		t.Fatalf("start nats container: %v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("nats host: %v", err)
	}
	port, err := container.MappedPort(ctx, "4222/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("nats port: %v", err)
	}
	integrationNATS.container = container
	integrationNATS.url = "nats://" + net.JoinHostPort(host, port.Port())
	return integrationNATS.url
}

func TestNATSIntegration_BindPayloadThroughWorker(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendNATS) {
		t.Skip("nats integration backend not selected")
	}
	type payload struct {
		ID int `json:"id"`
	}
	received := make(chan payload, 1)

	q, err := newQueueRuntime(natsCfg(ensureNATS(t)))
	if err != nil {
		t.Fatalf("new nats queue failed: %v", err)
	}
	q.Register("job:nats:bind", func(_ context.Context, job queue.Job) error {
		var in payload
		if err := job.Bind(&in); err != nil {
			return err
		}
		received <- in
		return nil
	})
	if err := withWorkers(q, 1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("nats queue start failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	want := payload{ID: 42}
	if err := q.DispatchCtx(context.Background(), queue.NewJob("job:nats:bind").Payload(want).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case got := <-received:
		if got != want {
			t.Fatalf("bind payload mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for nats worker bind payload")
	}
}

func TestNATSIntegration_OptionBehavior(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendNATS) {
		t.Skip("nats integration backend not selected")
	}
	delay := 200 * time.Millisecond
	backoff := 120 * time.Millisecond
	timeout := 80 * time.Millisecond
	started := time.Now()
	done := make(chan struct{}, 1)
	var calls atomic.Int32
	deadlineSeen := make(chan bool, 1)

	q, err := newQueueRuntime(natsCfg(ensureNATS(t)))
	if err != nil {
		t.Fatalf("new nats queue failed: %v", err)
	}
	q.Register("job:nats:opts", func(ctx context.Context, _ queue.Job) error {
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
		t.Fatalf("nats queue start failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	job := queue.NewJob("job:nats:opts").
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
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first nats handler attempt")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for nats retry flow")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
	elapsed := time.Since(started)
	if elapsed < delay+2*backoff-80*time.Millisecond {
		t.Fatalf("expected elapsed to include delay and backoff, got %s", elapsed)
	}
}

func TestNATSIntegration_UniqueDuplicate(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendNATS) {
		t.Skip("nats integration backend not selected")
	}
	q, err := newQueueRuntime(natsCfg(ensureNATS(t)))
	if err != nil {
		t.Fatalf("new nats queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	jobType := "job:nats:unique"
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
