package redisqueue

import (
	"context"
	"sync"

	"github.com/goforj/queue"
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

func newRedisWorker(server asynqServer, mux *asynq.ServeMux) queue.DriverWorkerBackend {
	return &redisWorker{server: server, mux: mux}
}

func (w *redisWorker) Register(jobType string, handler queue.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	w.mux.HandleFunc(jobType, func(ctx context.Context, job *asynq.Task) error {
		return handler(ctx, queue.NewJob(job.Type()).Payload(job.Payload()))
	})
}

func (w *redisWorker) StartWorkers(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
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

func (w *redisWorker) Shutdown(_ context.Context) error {
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
