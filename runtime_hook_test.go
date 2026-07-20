package queue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/queue/internal/runtimehook"
)

type runtimeHookWorkerStub struct{}

// Register satisfies the driver worker boundary for bridge tests.
func (runtimeHookWorkerStub) Register(string, Handler) {}

// StartWorkers satisfies the driver worker boundary for bridge tests.
func (runtimeHookWorkerStub) StartWorkers(context.Context) error { return nil }

// Shutdown satisfies the driver worker boundary for bridge tests.
func (runtimeHookWorkerStub) Shutdown(context.Context) error { return nil }

// TestBuildQueueFromDriverHookRejectsInvalidBridgeValues verifies the internal
// bridge fails closed when driver packages provide values outside its contract.
func TestBuildQueueFromDriverHookRejectsInvalidBridgeValues(t *testing.T) {
	backend := &driverQueueBackendStub{driver: DriverNull}
	validConfig := Config{Driver: DriverNull}

	tests := []struct {
		name          string
		config        any
		observer      any
		backend       any
		workerFactory runtimehook.WorkerFactory
		opts          []any
		want          string
	}{
		{
			name:    "config",
			config:  "not a config",
			backend: backend,
			want:    "invalid queue config type",
		},
		{
			name:     "observer",
			config:   validConfig,
			observer: "not an observer",
			backend:  backend,
			want:     "invalid queue observer type",
		},
		{
			name:    "backend",
			config:  validConfig,
			backend: "not a backend",
			want:    "invalid driver backend type",
		},
		{
			name:    "option",
			config:  validConfig,
			backend: backend,
			opts:    []any{"not an option"},
			want:    "invalid queue option type",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildQueueFromDriverHook(test.config, test.observer, test.backend, test.workerFactory, test.opts)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildQueueFromDriverHook() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestBuildQueueFromDriverHookBuildsAndExtractsRuntime verifies the bridge's
// successful path preserves the constructed runtime identity.
func TestBuildQueueFromDriverHookBuildsAndExtractsRuntime(t *testing.T) {
	raw, err := buildQueueFromDriverHook(
		Config{Driver: DriverNull},
		nil,
		&driverQueueBackendStub{driver: DriverNull},
		func(int) (any, error) { return runtimeHookWorkerStub{}, nil },
		[]any{WithWorkers(1)},
	)
	if err != nil {
		t.Fatalf("buildQueueFromDriverHook() error = %v", err)
	}
	q, ok := raw.(*Queue)
	if !ok {
		t.Fatalf("buildQueueFromDriverHook() type = %T, want *Queue", raw)
	}
	runtime, err := extractRuntimeFromQueueHook(q)
	if err != nil {
		t.Fatalf("extractRuntimeFromQueueHook() error = %v", err)
	}
	if runtime != q.q {
		t.Fatal("extractRuntimeFromQueueHook() returned a different runtime")
	}
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatalf("StartWorkers() error = %v", err)
	}
	if err := q.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

// TestBuildQueueFromDriverHookRejectsInvalidWorkerFactoryResults verifies the
// deferred factory boundary rejects absent and incompatible worker backends.
func TestBuildQueueFromDriverHookRejectsInvalidWorkerFactoryResults(t *testing.T) {
	tests := []struct {
		name    string
		factory runtimehook.WorkerFactory
		want    string
	}{
		{
			name: "nil worker backend",
			factory: func(int) (any, error) {
				return nil, nil
			},
			want: "driver worker factory returned nil",
		},
		{
			name: "wrong worker backend type",
			factory: func(int) (any, error) {
				return "not a worker backend", nil
			},
			want: "invalid worker backend type",
		},
		{
			name: "worker factory error",
			factory: func(int) (any, error) {
				return nil, errors.New("worker construction failed")
			},
			want: "worker construction failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := buildQueueFromDriverHook(Config{Driver: DriverNull}, nil, &driverQueueBackendStub{driver: DriverNull}, test.factory, nil)
			if err != nil {
				t.Fatalf("buildQueueFromDriverHook() error = %v", err)
			}
			q := raw.(*Queue)
			err = q.StartWorkers(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("StartWorkers() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestExtractRuntimeFromQueueHookRejectsInvalidValues verifies only initialized
// public queues can cross the internal test bridge.
func TestExtractRuntimeFromQueueHookRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "wrong type", value: "not a queue", want: "invalid queue type"},
		{name: "nil queue", value: (*Queue)(nil), want: "invalid queue type"},
		{name: "nil runtime", value: &Queue{}, want: "queue runtime is nil"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := extractRuntimeFromQueueHook(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("extractRuntimeFromQueueHook() error = %v, want %q", err, test.want)
			}
		})
	}
}
