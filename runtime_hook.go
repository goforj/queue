package queue

import (
	"fmt"

	"github.com/goforj/queue/internal/runtimehook"
)

// Register internal bridge/test hooks in package init to avoid import cycles.
func init() {
	runtimehook.BuildQueueFromDriver = buildQueueFromDriverHook
	runtimehook.ExtractRuntimeFromQueue = extractRuntimeFromQueueHook
}

func buildQueueFromDriverHook(cfgv any, backendv any, workerFactoryv runtimehook.WorkerFactory, optsv []any) (any, error) {
	cfg, ok := cfgv.(Config)
	if !ok {
		return nil, fmt.Errorf("invalid queue config type %T", cfgv)
	}
	backend, ok := backendv.(driverQueueBackend)
	if !ok {
		return nil, fmt.Errorf("invalid driver backend type %T", backendv)
	}

	var workerFactory driverWorkerFactory
	if workerFactoryv != nil {
		workerFactory = func(workers int) (driverWorkerBackend, error) {
			v, err := workerFactoryv(workers)
			if err != nil || v == nil {
				return nil, err
			}
			w, ok := v.(driverWorkerBackend)
			if !ok {
				return nil, fmt.Errorf("invalid worker backend type %T", v)
			}
			return w, nil
		}
	}

	opts := make([]Option, 0, len(optsv))
	for _, optv := range optsv {
		opt, ok := optv.(Option)
		if !ok {
			return nil, fmt.Errorf("invalid queue option type %T", optv)
		}
		opts = append(opts, opt)
	}

	raw, err := newQueueFromDriver(cfg, backend, workerFactory)
	if err != nil {
		return nil, err
	}
	return newQueueFromRuntime(raw, opts...)
}

func extractRuntimeFromQueueHook(qv any) (any, error) {
	q, ok := qv.(*Queue)
	if !ok || q == nil {
		return nil, fmt.Errorf("invalid queue type %T", qv)
	}
	if q.q == nil {
		return nil, fmt.Errorf("queue runtime is nil")
	}
	return q.q, nil
}
