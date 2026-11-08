package queue

import (
	"fmt"

	"github.com/goforj/queue/internal/runtimehook"
)

// Register internal bridge/test hooks in package init to avoid import cycles.
func init() {
	runtimehook.BuildQueueFromDriver = buildQueueFromDriverHook
	runtimehook.BuildQueueFromDescriptor = buildQueueFromDescriptorHook
	runtimehook.ExtractRuntimeFromQueue = extractRuntimeFromQueueHook
}

// buildQueueFromDriverHook retains built-in coordination for compatibility callers.
func buildQueueFromDriverHook(cfgv any, observerv any, backendv any, workerFactoryv runtimehook.WorkerFactory, optsv []any) (any, error) {
	return buildQueueFromDescriptorHook(cfgv, observerv, runtimehook.Descriptor{
		Backend:       backendv,
		WorkerFactory: workerFactoryv,
		WorkflowMode:  runtimehook.WorkflowBuiltIn,
	}, optsv)
}

// buildQueueFromDescriptorHook validates mode and ownership before attaching capabilities.
func buildQueueFromDescriptorHook(cfgv any, observerv any, descriptor runtimehook.Descriptor, optsv []any) (any, error) {
	cfg, ok := cfgv.(Config)
	if !ok {
		return nil, fmt.Errorf("invalid queue config type %T", cfgv)
	}
	observer, ok := observerv.(Observer)
	if !ok && observerv != nil {
		return nil, fmt.Errorf("invalid queue observer type %T", observerv)
	}
	backend, ok := descriptor.Backend.(driverQueueBackend)
	if !ok {
		return nil, fmt.Errorf("invalid driver backend type %T", descriptor.Backend)
	}

	var workerFactory driverWorkerFactory
	if descriptor.WorkerFactory != nil {
		workerFactory = func(workers int) (driverWorkerBackend, error) {
			v, err := descriptor.WorkerFactory(workers)
			if err != nil {
				return nil, err
			}
			if v == nil {
				return nil, fmt.Errorf("driver worker factory returned nil")
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

	if err := validateDriverDescriptor(cfg, backend, descriptor); err != nil {
		return nil, err
	}
	var settings runtimeOptions
	settings.apply(opts)
	if descriptor.ValidateSettings != nil {
		if err := descriptor.ValidateSettings(settings.driverSettings()); err != nil {
			return nil, err
		}
	}
	raw, err := newQueueFromDriver(cfg, observer, backend, workerFactory)
	if err != nil {
		return nil, err
	}
	return newQueueWithDescriptor(raw, settings, descriptor)
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

// validateDriverDescriptor rejects ambiguous authority before installing any delivery routes.
func validateDriverDescriptor(cfg Config, backend driverQueueBackend, descriptor runtimehook.Descriptor) error {
	if cfg.Driver != backend.Driver() {
		return fmt.Errorf("driver descriptor identity %q does not match backend %q", cfg.Driver, backend.Driver())
	}
	switch descriptor.WorkflowMode {
	case runtimehook.WorkflowBuiltIn, runtimehook.WorkflowUnsupported:
		if descriptor.NewCoordinator != nil {
			return fmt.Errorf("workflow mode %q cannot supply a native coordinator", descriptor.WorkflowMode)
		}
	case runtimehook.WorkflowNative:
		if descriptor.NewCoordinator == nil {
			return fmt.Errorf("native workflow mode requires a coordinator factory")
		}
	default:
		return fmt.Errorf("driver descriptor requires an explicit workflow mode, got %q", descriptor.WorkflowMode)
	}
	return nil
}
