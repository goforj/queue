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
	sw, err := NewWorker(WorkerConfig{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new sync worker failed: %v", err)
	}
	if sw.Driver() != DriverSync {
		t.Fatal("expected sync worker driver")
	}
	wp, err := NewWorker(WorkerConfig{
		Driver:        DriverWorkerpool,
		Workers:       1,
		QueueCapacity: 2,
	})
	if err != nil {
		t.Fatalf("new workerpool worker failed: %v", err)
	}
	if wp.Driver() != DriverWorkerpool {
		t.Fatal("expected workerpool worker driver")
	}
	dw, err := NewWorker(WorkerConfig{
		Driver:         DriverDatabase,
		DatabaseDriver: "sqlite",
		DatabaseDSN:    t.TempDir() + "/worker.db",
	})
	if err != nil {
		t.Fatalf("new database worker failed: %v", err)
	}
	if dw.Driver() != DriverDatabase {
		t.Fatal("expected database worker driver")
	}
	rw, err := NewWorker(WorkerConfig{
		Driver:    DriverRedis,
		RedisAddr: "127.0.0.1:6379",
	})
	if err != nil {
		t.Fatalf("new redis worker failed: %v", err)
	}
	if rw.Driver() != DriverRedis {
		t.Fatal("expected redis worker driver")
	}
	nw, err := NewWorker(WorkerConfig{
		Driver:  DriverNATS,
		NATSURL: "nats://127.0.0.1:4222",
	})
	if err != nil {
		t.Fatalf("new nats worker failed: %v", err)
	}
	if nw.Driver() != DriverNATS {
		t.Fatal("expected nats worker driver")
	}
}

func TestNewWorker_UnknownDriverFails(t *testing.T) {
	worker, err := NewWorker(WorkerConfig{Driver: Driver("unknown")})
	if err == nil {
		t.Fatal("expected unknown driver error")
	}
	if worker != nil {
		t.Fatal("expected nil worker")
	}
}

func TestNewWorker_RedisRequiresConn(t *testing.T) {
	worker, err := NewWorker(WorkerConfig{Driver: DriverRedis})
	if err == nil {
		t.Fatal("expected redis addr error")
	}
	if worker != nil {
		t.Fatal("expected nil worker")
	}
}

func TestNewWorker_NATSRequiresURL(t *testing.T) {
	worker, err := NewWorker(WorkerConfig{Driver: DriverNATS})
	if err == nil {
		t.Fatal("expected nats url error")
	}
	if worker != nil {
		t.Fatal("expected nil worker")
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
		if string(task.PayloadBytes()) != "hello" {
			t.Fatalf("unexpected payload: %s", string(task.PayloadBytes()))
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
