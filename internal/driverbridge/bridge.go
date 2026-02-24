package driverbridge

import (
	"context"
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/runtimehook"
)

type queueBackend interface {
	Driver() queue.Driver
	Dispatch(ctx context.Context, job queue.Job) error
	Shutdown(ctx context.Context) error
}

type runtimeQueueBackend interface {
	queueBackend
	Register(jobType string, handler queue.Handler)
	StartWorkers(ctx context.Context) error
}

type workerBackend interface {
	Register(jobType string, handler queue.Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// NewQueueFromDriver builds a high-level *queue.Queue from a driver backend.
//
// The helper keeps driver modules off the public low-level constructor path while
// the runtime seam is being moved behind internal APIs.
func NewQueueFromDriver(
	cfg queue.Config,
	backend any,
	workerFactory func(workers int) (any, error),
	opts ...queue.Option,
) (*queue.Queue, error) {
	driverBackend, err := adaptQueueBackend(backend)
	if err != nil {
		return nil, err
	}
	if runtimehook.BuildQueueFromDriver == nil {
		return nil, fmt.Errorf("queue internal runtime hook is not registered")
	}
	rawOpts := make([]any, 0, len(opts))
	for _, opt := range opts {
		rawOpts = append(rawOpts, opt)
	}
	qv, err := runtimehook.BuildQueueFromDriver(cfg, driverBackend, adaptWorkerFactory(workerFactory), rawOpts)
	if err != nil {
		return nil, err
	}
	q, ok := qv.(*queue.Queue)
	if !ok {
		return nil, fmt.Errorf("queue internal runtime hook returned %T", qv)
	}
	return q, nil
}

type queueBackendAdapter struct {
	inner queueBackend
}

func (a queueBackendAdapter) Driver() queue.Driver { return a.inner.Driver() }
func (a queueBackendAdapter) Dispatch(ctx context.Context, job queue.Job) error {
	return a.inner.Dispatch(ctx, job)
}
func (a queueBackendAdapter) Shutdown(ctx context.Context) error { return a.inner.Shutdown(ctx) }
func (a queueBackendAdapter) Pause(ctx context.Context, queueName string) error {
	controller, ok := a.inner.(queue.QueueController)
	if !ok {
		return queue.ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}
func (a queueBackendAdapter) Resume(ctx context.Context, queueName string) error {
	controller, ok := a.inner.(queue.QueueController)
	if !ok {
		return queue.ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}
func (a queueBackendAdapter) Stats(ctx context.Context) (queue.StatsSnapshot, error) {
	provider, ok := a.inner.(queue.StatsProvider)
	if !ok {
		return queue.StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", a.Driver())
	}
	return provider.Stats(ctx)
}

type runtimeQueueBackendAdapter struct {
	queueBackendAdapter
	inner runtimeQueueBackend
}

func (a runtimeQueueBackendAdapter) Register(jobType string, handler queue.Handler) {
	a.inner.Register(jobType, handler)
}
func (a runtimeQueueBackendAdapter) StartWorkers(ctx context.Context) error {
	return a.inner.StartWorkers(ctx)
}
func (a runtimeQueueBackendAdapter) Pause(ctx context.Context, queueName string) error {
	return a.queueBackendAdapter.Pause(ctx, queueName)
}
func (a runtimeQueueBackendAdapter) Resume(ctx context.Context, queueName string) error {
	return a.queueBackendAdapter.Resume(ctx, queueName)
}
func (a runtimeQueueBackendAdapter) Stats(ctx context.Context) (queue.StatsSnapshot, error) {
	return a.queueBackendAdapter.Stats(ctx)
}

type workerBackendAdapter struct {
	inner workerBackend
}

func (a workerBackendAdapter) Register(jobType string, handler queue.Handler) {
	a.inner.Register(jobType, handler)
}
func (a workerBackendAdapter) StartWorkers(ctx context.Context) error {
	return a.inner.StartWorkers(ctx)
}
func (a workerBackendAdapter) Shutdown(ctx context.Context) error { return a.inner.Shutdown(ctx) }

func adaptQueueBackend(v any) (any, error) {
	if v == nil {
		return nil, fmt.Errorf("driver backend is nil")
	}
	if native, ok := v.(runtimeQueueBackend); ok {
		return runtimeQueueBackendAdapter{
			queueBackendAdapter: queueBackendAdapter{inner: native},
			inner:               native,
		}, nil
	}
	if basic, ok := v.(queueBackend); ok {
		return queueBackendAdapter{inner: basic}, nil
	}
	return nil, fmt.Errorf("unsupported driver backend type %T", v)
}

func adaptWorkerFactory(fn func(int) (any, error)) runtimehook.WorkerFactory {
	if fn == nil {
		return nil
	}
	return func(workers int) (any, error) {
		v, err := fn(workers)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, nil
		}
		w, ok := v.(workerBackend)
		if !ok {
			return nil, fmt.Errorf("unsupported driver worker backend type %T", v)
		}
		return workerBackendAdapter{inner: w}, nil
	}
}
