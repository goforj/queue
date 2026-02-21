package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
)

type asynqServerStub struct {
	startErr         error
	startCalls       int
	shutdownCalls    int
	lastStartHandler asynq.Handler
}

func (s *asynqServerStub) Start(handler asynq.Handler) error {
	s.startCalls++
	s.lastStartHandler = handler
	return s.startErr
}

func (s *asynqServerStub) Shutdown() { s.shutdownCalls++ }
func (s *asynqServerStub) Stop()     {}

func TestRedisWorker_RegisterStartShutdownBranches(t *testing.T) {
	server := &asynqServerStub{}
	mux := asynq.NewServeMux()
	w := newRedisWorker(server, mux).(*redisWorker)

	// Register no-op branches.
	w.Register("", func(context.Context, Task) error { return nil })
	w.Register("job:nil", nil)
	w.Register("job:ok", func(context.Context, Task) error { return nil })

	// Start with canceled context branch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.StartWorkers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("second start should be idempotent, got %v", err)
	}
	if server.startCalls != 1 {
		t.Fatalf("expected one start call, got %d", server.startCalls)
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown should be idempotent, got %v", err)
	}
	if server.shutdownCalls != 1 {
		t.Fatalf("expected one shutdown call, got %d", server.shutdownCalls)
	}
}

func TestRedisWorker_StartError(t *testing.T) {
	server := &asynqServerStub{startErr: errors.New("start failed")}
	w := newRedisWorker(server, asynq.NewServeMux()).(*redisWorker)

	if err := w.StartWorkers(context.Background()); err == nil {
		t.Fatal("expected start error")
	}
	if w.started {
		t.Fatal("worker should remain not started on start error")
	}
}
