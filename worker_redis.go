package queue

import (
	"context"
	"sync"

	"github.com/hibiken/asynq"
)

type asynqServer interface {
	Start(handler asynq.Handler) error
	Shutdown()
	Stop()
}

type redisWorker struct {
	server asynqServer
	mux    *asynq.ServeMux

	mu      sync.Mutex
	started bool
}

func newRedisWorker(server asynqServer, mux *asynq.ServeMux) workerRuntime {
	return &redisWorker{server: server, mux: mux}
}

func (w *redisWorker) Driver() Driver {
	return DriverRedis
}

func (w *redisWorker) Register(taskType string, handler Handler) {
	if taskType == "" || handler == nil {
		return
	}
	w.mux.HandleFunc(taskType, func(ctx context.Context, task *asynq.Task) error {
		return handler(ctx, NewTask(task.Type()).Payload(task.Payload()))
	})
}

func (w *redisWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return nil
	}
	if err := w.server.Start(w.mux); err != nil {
		return err
	}
	w.started = true
	return nil
}

func (w *redisWorker) Shutdown() error {
	w.mu.Lock()
	started := w.started
	w.started = false
	w.mu.Unlock()

	if !started {
		return nil
	}
	w.server.Shutdown()
	return nil
}
