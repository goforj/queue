package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
)

type fakeAsynqServer struct {
	startErr error
	started  bool
	stopped  bool
}

func (s *fakeAsynqServer) Start(_ asynq.Handler) error {
	if s.startErr != nil {
		return s.startErr
	}
	s.started = true
	return nil
}

func (s *fakeAsynqServer) Shutdown() {
	s.stopped = true
}

func (s *fakeAsynqServer) Stop() {
	s.stopped = true
}

func TestNewWorkerAdapters(t *testing.T) {
	if NewSyncWorker().Driver() != DriverSync {
		t.Fatal("expected sync worker driver")
	}
	if NewWorkerpoolWorker(WorkerpoolConfig{Workers: 1, Buffer: 2}).Driver() != DriverWorkerpool {
		t.Fatal("expected workerpool worker driver")
	}
	dw, err := NewDatabaseWorker(DatabaseConfig{DriverName: "sqlite", DSN: t.TempDir() + "/worker.db"})
	if err != nil {
		t.Fatalf("new database worker failed: %v", err)
	}
	if dw.Driver() != DriverDatabase {
		t.Fatal("expected database worker driver")
	}
}

func TestRedisWorker_StartShutdown(t *testing.T) {
	server := &fakeAsynqServer{}
	worker := newRedisWorker(server, asynq.NewServeMux())
	if err := worker.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if !server.started {
		t.Fatal("expected server start")
	}
	if err := worker.Shutdown(); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if !server.stopped {
		t.Fatal("expected server shutdown")
	}
}

func TestRedisWorker_StartError(t *testing.T) {
	server := &fakeAsynqServer{startErr: errors.New("boom")}
	worker := newRedisWorker(server, asynq.NewServeMux())
	if err := worker.Start(); err == nil {
		t.Fatal("expected start error")
	}
}

func TestRedisWorker_RegisterRoutesPayload(t *testing.T) {
	server := &fakeAsynqServer{}
	mux := asynq.NewServeMux()
	worker := newRedisWorker(server, mux)

	triggered := false
	worker.Register("job:test", func(_ context.Context, task Task) error {
		triggered = true
		if task.Type != "job:test" {
			t.Fatalf("unexpected type: %s", task.Type)
		}
		if string(task.Payload) != "hello" {
			t.Fatalf("unexpected payload: %s", string(task.Payload))
		}
		return nil
	})

	h, _ := mux.Handler(asynq.NewTask("job:test", []byte("hello")))
	if err := h.ProcessTask(context.Background(), asynq.NewTask("job:test", []byte("hello"))); err != nil {
		t.Fatalf("process task failed: %v", err)
	}
	if !triggered {
		t.Fatal("expected handler call")
	}
}
