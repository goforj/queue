package queue

import (
	"context"
	"fmt"
)

// DriverQueueBackend is the low-level backend interface optional driver modules implement.
//
// This is primarily intended for driver-module integration, not normal application code.
// @group Queue Runtime
type DriverQueueBackend interface {
	Driver() Driver
	Dispatch(ctx context.Context, job Job) error
	Shutdown(ctx context.Context) error
}

// DriverRuntimeQueueBackend is a queue backend that can also run workers directly.
// This is used by SQL-backed driver modules and other runtimes that own their worker loop.
// @group Queue Runtime
type DriverRuntimeQueueBackend interface {
	DriverQueueBackend
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
}

// DriverWorkerBackend is the worker backend interface optional driver modules implement.
//
// This is primarily intended for driver-module integration, not normal application code.
// @group Queue Runtime
type DriverWorkerBackend interface {
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// DriverWorkerFactory builds a driver worker backend for an external queue runtime.
// @group Queue Runtime
type DriverWorkerFactory func(cfg Config, workers int) (DriverWorkerBackend, error)

// NewQueueFromDriver wraps a driver-module backend into a QueueRuntime.
//
// Driver modules use this to return a queue.QueueRuntime without relying on root optional
// driver factory support.
// @group Queue Runtime
func NewQueueFromDriver(cfg Config, backend DriverQueueBackend, workerFactory DriverWorkerFactory) (QueueRuntime, error) {
	if backend == nil {
		return nil, fmt.Errorf("driver backend is nil")
	}
	cfg = cfg.normalize()

	var q queueBackend
	var runtime runtimeQueueBackend
	if native, ok := backend.(DriverRuntimeQueueBackend); ok {
		runtime = driverRuntimeQueueBackendAdapter{native}
		q = runtime
	} else {
		q = driverQueueBackendAdapter{backend}
	}

	common := &queueCommon{
		inner:  newObservedQueue(q, cfg.Driver, cfg.Observer),
		cfg:    cfg,
		driver: cfg.Driver,
	}
	if runtime != nil {
		return &nativeQueueRuntime{
			common:     common,
			runtime:    runtime,
			registered: make(map[string]Handler),
		}, nil
	}
	return &externalQueueRuntime{
		common:     common,
		registered: make(map[string]Handler),
		newWorker:  workerFactory,
	}, nil
}

