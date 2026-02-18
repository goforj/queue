//go:build integration

package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func newSQSIntegrationConfig() Config {
	return Config{
		Driver:       DriverSQS,
		SQSEndpoint:  integrationSQS.endpoint,
		SQSRegion:    integrationSQS.region,
		SQSAccessKey: integrationSQS.accessKey,
		SQSSecretKey: integrationSQS.secretKey,
	}
}

func newSQSWorkerIntegrationConfig() WorkerConfig {
	return WorkerConfig{
		Driver:       DriverSQS,
		SQSEndpoint:  integrationSQS.endpoint,
		SQSRegion:    integrationSQS.region,
		SQSAccessKey: integrationSQS.accessKey,
		SQSSecretKey: integrationSQS.secretKey,
		DefaultQueue: "default",
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

	worker, err := NewWorker(newSQSWorkerIntegrationConfig())
	if err != nil {
		t.Fatalf("new sqs worker failed: %v", err)
	}
	worker.Register("job:sqs:bind", func(_ context.Context, task Task) error {
		var in payload
		if err := task.Bind(&in); err != nil {
			return err
		}
		received <- in
		return nil
	})
	if err := worker.Start(); err != nil {
		t.Fatalf("sqs worker start failed: %v", err)
	}
	t.Cleanup(func() { _ = worker.Shutdown() })

	q, err := New(newSQSIntegrationConfig())
	if err != nil {
		t.Fatalf("new sqs queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	want := payload{ID: 42}
	if err := q.Enqueue(context.Background(), NewTask("job:sqs:bind").Payload(want).OnQueue("default")); err != nil {
		t.Fatalf("enqueue failed: %v", err)
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

	worker, err := NewWorker(newSQSWorkerIntegrationConfig())
	if err != nil {
		t.Fatalf("new sqs worker failed: %v", err)
	}
	worker.Register("job:sqs:opts", func(ctx context.Context, _ Task) error {
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
	if err := worker.Start(); err != nil {
		t.Fatalf("sqs worker start failed: %v", err)
	}
	t.Cleanup(func() { _ = worker.Shutdown() })

	q, err := New(newSQSIntegrationConfig())
	if err != nil {
		t.Fatalf("new sqs queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	task := NewTask("job:sqs:opts").
		Payload([]byte("opts")).
		OnQueue("default").
		Delay(delay).
		Timeout(timeout).
		Retry(2).
		Backoff(backoff)
	if err := q.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("enqueue failed: %v", err)
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
	q, err := New(newSQSIntegrationConfig())
	if err != nil {
		t.Fatalf("new sqs queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	taskType := "job:sqs:unique"
	payload := []byte("same")
	first := NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(500 * time.Millisecond)
	if err := q.Enqueue(context.Background(), first); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	second := NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(500 * time.Millisecond)
	err = q.Enqueue(context.Background(), second)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if err := q.Enqueue(context.Background(), second); err != nil {
		t.Fatalf("enqueue after ttl failed: %v", err)
	}
}
