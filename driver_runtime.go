package queue

import (
	"context"
	"fmt"
)

type driverQueueBackend interface {
	Driver() Driver
	Dispatch(ctx context.Context, job Job) error
	Shutdown(ctx context.Context) error
}

type driverRuntimeQueueBackend interface {
	driverQueueBackend
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
}

type driverWorkerBackend interface {
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

type driverWorkerFactory func(workers int) (driverWorkerBackend, error)

func newQueueFromDriver(cfg Config, backend driverQueueBackend, workerFactory driverWorkerFactory) (queueRuntime, error) {
	if backend == nil {
		return nil, fmt.Errorf("driver backend is nil")
	}
	cfg = cfg.normalize()

	var q queueBackend
	var runtime runtimeQueueBackend
	if native, ok := backend.(driverRuntimeQueueBackend); ok {
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
