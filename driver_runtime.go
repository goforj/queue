package queue

import (
	"context"
	"fmt"

	"github.com/goforj/queue/busruntime"
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
	DrainWorkers(ctx context.Context) error
}

type driverWorkerBackend interface {
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

type driverWorkerFactory func(workers int) (driverWorkerBackend, error)

func newQueueFromDriver(cfg Config, observer Observer, backend driverQueueBackend, workerFactory driverWorkerFactory) (queueRuntime, error) {
	if backend == nil {
		return nil, fmt.Errorf("driver backend is nil")
	}
	cfg = cfg.normalize()
	observer = ensureObserverSink(observer)

	var q queueBackend
	var runtime runtimeQueueBackend
	if native, ok := backend.(driverRuntimeQueueBackend); ok {
		runtime = driverRuntimeQueueBackendAdapter{native}
		q = runtime
	} else {
		q = driverQueueBackendAdapter{backend}
	}

	common := &queueCommon{
		inner:        newObservedQueue(q, cfg.Driver, observer),
		cfg:          cfg,
		driver:       cfg.Driver,
		observerSink: observer,
	}
	if runtime != nil {
		return &nativeQueueRuntime{
			common:  common,
			runtime: runtime,
			nativeQueueRuntimeState: &nativeQueueRuntimeState{
				registered:   make(map[string]Handler),
				continuation: busruntime.NewContinuationScope(),
			},
		}, nil
	}
	return &externalQueueRuntime{
		common:    common,
		newWorker: workerFactory,
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered:   make(map[string]Handler),
			continuation: busruntime.NewContinuationScope(),
		},
	}, nil
}
