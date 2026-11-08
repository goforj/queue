package driverbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/runtimehook"
	"github.com/goforj/queue/internal/workflow"
)

type descriptorBackend struct {
	nativeBackendStub
	routes     []string
	dispatches int
}

// Register records exactly which coordinator routes root construction installs.
func (b *descriptorBackend) Register(name string, _ queue.Handler) { b.routes = append(b.routes, name) }

// Dispatch counts acceptance so unsupported workflows cannot silently submit work.
func (b *descriptorBackend) Dispatch(context.Context, queue.Job) error { b.dispatches++; return nil }

type descriptorCoordinator struct {
	binding *runtimehook.WorkflowBinding
}

// Binding identifies the runtime/executor pair selected by root construction.
func (c *descriptorCoordinator) Binding() *runtimehook.WorkflowBinding { return c.binding }

// FindChain proves state operations delegate through the preserved native capability.
func (c *descriptorCoordinator) FindChain(context.Context, string) (workflow.ChainState, error) {
	return workflow.ChainState{ChainID: "native-state"}, nil
}

type descriptorChain struct{ queue string }

// OnQueue records native builder configuration through the public adapter.
func (b *descriptorChain) OnQueue(name string) workflow.ChainBuilder { b.queue = name; return b }

// Catch preserves fluent native builder identity.
func (b *descriptorChain) Catch(func(context.Context, workflow.ChainState, error) error) workflow.ChainBuilder {
	return b
}

// Finally preserves fluent native builder identity.
func (b *descriptorChain) Finally(func(context.Context, workflow.ChainState) error) workflow.ChainBuilder {
	return b
}

// Dispatch distinguishes native dispatch from any built-in fallback.
func (b *descriptorChain) Dispatch(context.Context) (string, error) {
	return "native-chain/" + b.queue, nil
}

type descriptorBatch struct {
	name, queue string
	allowed     bool
}

// Name records native builder configuration through the public adapter.
func (b *descriptorBatch) Name(name string) workflow.BatchBuilder { b.name = name; return b }

// OnQueue records native routing through the public adapter.
func (b *descriptorBatch) OnQueue(name string) workflow.BatchBuilder { b.queue = name; return b }

// AllowFailures preserves the native aggregate policy.
func (b *descriptorBatch) AllowFailures() workflow.BatchBuilder { b.allowed = true; return b }

// Progress preserves fluent native builder identity.
func (b *descriptorBatch) Progress(func(context.Context, workflow.BatchState) error) workflow.BatchBuilder {
	return b
}

// Then preserves fluent native builder identity.
func (b *descriptorBatch) Then(func(context.Context, workflow.BatchState) error) workflow.BatchBuilder {
	return b
}

// Catch preserves fluent native builder identity.
func (b *descriptorBatch) Catch(func(context.Context, workflow.BatchState, error) error) workflow.BatchBuilder {
	return b
}

// Finally preserves fluent native builder identity.
func (b *descriptorBatch) Finally(func(context.Context, workflow.BatchState) error) workflow.BatchBuilder {
	return b
}

// Dispatch exposes all forwarded policy without a physical queue submission.
func (b *descriptorBatch) Dispatch(context.Context) (string, error) {
	return fmt.Sprintf("native-batch/%s/%s/%t", b.name, b.queue, b.allowed), nil
}

// Chain supplies the driver-owned builder independently of root storage.
func (*descriptorCoordinator) Chain(...workflow.Job) workflow.ChainBuilder { return &descriptorChain{} }

// Batch supplies the driver-owned builder independently of root storage.
func (*descriptorCoordinator) Batch(...workflow.Job) workflow.BatchBuilder { return &descriptorBatch{} }

// FindBatch distinguishes native state reads from built-in lookup.
func (*descriptorCoordinator) FindBatch(context.Context, string) (workflow.BatchState, error) {
	return workflow.BatchState{BatchID: "native-batch-state"}, nil
}

// Prune preserves a native driver's capability refusal through the public facade.
func (*descriptorCoordinator) Prune(context.Context, time.Time) error {
	return queue.ErrWorkflowUnsupported
}

// newDescriptorBackend creates a resource with a driver identity outside the built-in catalog.
func newDescriptorBackend() *descriptorBackend {
	return &descriptorBackend{nativeBackendStub: nativeBackendStub{externalBackendStub: externalBackendStub{driver: "test-native"}}}
}

// TestDescriptorUnsupportedHasNoCoordinatorFallback proves ordinary acceptance
// remains available while every portable workflow boundary rejects before acceptance.
func TestDescriptorUnsupportedHasNoCoordinatorFallback(t *testing.T) {
	backend := newDescriptorBackend()
	q, err := NewQueue(queue.Config{Driver: backend.Driver()}, nil, runtimehook.Descriptor{
		Backend: backend, WorkflowMode: runtimehook.WorkflowUnsupported,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	if len(backend.routes) != 1 || backend.routes[0] != workflow.DirectDeliveryType {
		t.Fatalf("unsupported mode installed coordinator routes: %v", backend.routes)
	}
	_, chainErr := q.Chain(queue.NewJob("one")).OnQueue("reports").Finally(nil).Dispatch(t.Context())
	_, batchErr := q.Batch(queue.NewJob("one")).Name("reports").AllowFailures().Progress(nil).Then(nil).Catch(nil).Finally(nil).Dispatch(t.Context())
	_, findChainErr := q.FindChain(t.Context(), "chain")
	_, findBatchErr := q.FindBatch(t.Context(), "batch")
	pruneErr := q.Prune(t.Context(), time.Now())
	for name, err := range map[string]error{"chain": chainErr, "batch": batchErr, "find chain": findChainErr, "find batch": findBatchErr, "prune": pruneErr} {
		if !errors.Is(err, queue.ErrWorkflowUnsupported) {
			t.Errorf("%s error = %v", name, err)
		}
	}
	if backend.dispatches != 0 {
		t.Fatal("unsupported workflow was accepted")
	}
	q.Register("one", func(context.Context, queue.Message) error { return nil })
	if _, err := q.Dispatch(queue.NewJob("one")); err != nil {
		t.Fatal(err)
	}
	if backend.dispatches != 1 {
		t.Fatalf("ordinary dispatches = %d", backend.dispatches)
	}
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := q.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := q.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !backend.started || !backend.stopped {
		t.Fatal("physical lifecycle did not remain independent")
	}
}

// TestDescriptorNativeBindingSurvivesBridge proves adaptation cannot erase
// explicit native coordination or replace its resource identity and executor.
func TestDescriptorNativeBindingSurvivesBridge(t *testing.T) {
	backend := newDescriptorBackend()
	var binding *runtimehook.WorkflowBinding
	q, err := NewQueue(queue.Config{Driver: backend.Driver()}, nil, runtimehook.Descriptor{
		Backend: backend, WorkflowMode: runtimehook.WorkflowNative,
		NewCoordinator: func(selected *runtimehook.WorkflowBinding) (runtimehook.NativeCoordinator, error) {
			binding = selected
			return &descriptorCoordinator{binding: selected}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Driver() != string(backend.Driver()) || binding.Jobs() == nil || binding.Runtime() == nil {
		t.Fatal("incomplete native binding")
	}
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	if len(backend.routes) != 1 {
		t.Fatalf("native mode installed built-in routes: %v", backend.routes)
	}
	state, err := q.FindChain(t.Context(), "any")
	if err != nil || state.ChainID != "native-state" {
		t.Fatalf("native state = %+v, err = %v", state, err)
	}
	chainID, err := q.Chain(queue.NewJob("one")).OnQueue("reports").Catch(nil).Finally(nil).Dispatch(t.Context())
	if err != nil || chainID != "native-chain/reports" {
		t.Fatalf("native chain=%q err=%v", chainID, err)
	}
	batchID, err := q.Batch(queue.NewJob("one")).Name("summary").OnQueue("reports").AllowFailures().Progress(nil).Then(nil).Catch(nil).Finally(nil).Dispatch(t.Context())
	if err != nil || batchID != "native-batch/summary/reports/true" {
		t.Fatalf("native batch=%q err=%v", batchID, err)
	}
	batch, err := q.FindBatch(t.Context(), "any")
	if err != nil || batch.BatchID != "native-batch-state" {
		t.Fatalf("native batch state=%+v err=%v", batch, err)
	}
	if err := q.Prune(t.Context(), time.Now()); !errors.Is(err, queue.ErrWorkflowUnsupported) {
		t.Fatalf("native prune=%v", err)
	}
	if backend.dispatches != 0 {
		t.Fatal("native coordination fell through to physical built-in delivery")
	}
}

// TestDescriptorRejectsInvalidAuthority covers every descriptor validation failure
// before registrations, worker creation or acceptance can occur.
func TestDescriptorRejectsInvalidAuthority(t *testing.T) {
	factoryErr := errors.New("coordinator construction failed")
	factory := func(b *runtimehook.WorkflowBinding) (runtimehook.NativeCoordinator, error) {
		return &descriptorCoordinator{binding: b}, nil
	}
	tests := []struct {
		name    string
		mode    runtimehook.WorkflowMode
		factory func(*runtimehook.WorkflowBinding) (runtimehook.NativeCoordinator, error)
		driver  queue.Driver
		want    string
	}{
		{name: "omitted mode", want: "explicit workflow mode"},
		{name: "unknown mode", mode: "other", want: "explicit workflow mode"},
		{name: "native without factory", mode: runtimehook.WorkflowNative, want: "requires a coordinator"},
		{name: "built-in with native factory", mode: runtimehook.WorkflowBuiltIn, factory: factory, want: "cannot supply"},
		{name: "unsupported with factory", mode: runtimehook.WorkflowUnsupported, factory: factory, want: "cannot supply"},
		{name: "driver mismatch", mode: runtimehook.WorkflowBuiltIn, driver: queue.DriverRedis, want: "does not match backend"},
		{name: "factory error", mode: runtimehook.WorkflowNative, factory: func(*runtimehook.WorkflowBinding) (runtimehook.NativeCoordinator, error) { return nil, factoryErr }, want: factoryErr.Error()},
		{name: "nil coordinator", mode: runtimehook.WorkflowNative, factory: func(*runtimehook.WorkflowBinding) (runtimehook.NativeCoordinator, error) { return nil, nil }, want: "does not belong"},
		{name: "different binding", mode: runtimehook.WorkflowNative, factory: func(*runtimehook.WorkflowBinding) (runtimehook.NativeCoordinator, error) {
			return &descriptorCoordinator{binding: runtimehook.NewWorkflowBinding(nil, nil, "other")}, nil
		}, want: "does not belong"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newDescriptorBackend()
			driver := test.driver
			if driver == "" {
				driver = backend.Driver()
			}
			_, err := NewQueue(queue.Config{Driver: driver}, nil, runtimehook.Descriptor{Backend: backend, WorkflowMode: test.mode, NewCoordinator: test.factory})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("constructor error = %v, want %q", err, test.want)
			}
			if backend.dispatches != 0 || backend.started {
				t.Fatal("invalid authority accepted work or started workers")
			}
		})
	}
}

// TestDescriptorValidatesTypedSettingsOnce proves explicit nil overrides and
// configured execution options reach the validator before any route is installed.
func TestDescriptorValidatesTypedSettingsOnce(t *testing.T) {
	backend := newDescriptorBackend()
	rejected := errors.New("store override unsupported")
	var calls int
	_, err := NewQueue(queue.Config{Driver: backend.Driver()}, nil, runtimehook.Descriptor{
		Backend: backend, WorkflowMode: runtimehook.WorkflowUnsupported,
		ValidateSettings: func(settings runtimehook.Settings) error {
			calls++
			if !settings.StoreSupplied || settings.Store != nil || !settings.ClockSupplied || settings.Clock != nil || settings.Workers != 3 || !settings.MiddlewareSupplied || !settings.ObserverSupplied || !settings.DecoratorSupplied || !settings.LegacyEnvelopeSupplied || !settings.LegacyDirectEnvelope {
				t.Fatalf("settings lost provenance: %+v", settings)
			}
			return rejected
		},
	}, queue.WithStore(nil), queue.WithClock(nil), queue.WithWorkers(3), queue.WithMiddleware(), queue.WithObserver(nil), queue.WithHandlerContextDecorator(nil), queue.WithLegacyDirectEnvelope())
	if !errors.Is(err, rejected) || calls != 1 || len(backend.routes) != 0 {
		t.Fatalf("validation err=%v calls=%d routes=%v", err, calls, backend.routes)
	}
}

// TestLegacyBridgeExplicitlySelectsBuiltIn proves existing optional drivers keep
// their coordinator while only new descriptor callers must specify the mode.
func TestLegacyBridgeExplicitlySelectsBuiltIn(t *testing.T) {
	backend := newDescriptorBackend()
	q, err := NewQueueFromDriver(queue.Config{Driver: backend.Driver()}, nil, backend, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	if len(backend.routes) != 4 {
		t.Fatalf("built-in routes = %v", backend.routes)
	}
	_, err = q.FindChain(t.Context(), "missing")
	if !errors.Is(err, queue.ErrWorkflowNotFound) {
		t.Fatalf("built-in read = %v", err)
	}
}

// TestDescriptorHookFailures preserves explicit bridge failures instead of
// silently constructing a queue through a capability-erasing legacy path.
func TestDescriptorHookFailures(t *testing.T) {
	original := runtimehook.BuildQueueFromDescriptor
	t.Cleanup(func() { runtimehook.BuildQueueFromDescriptor = original })
	backend := newDescriptorBackend()
	descriptor := runtimehook.Descriptor{Backend: backend, WorkflowMode: runtimehook.WorkflowBuiltIn}
	runtimehook.BuildQueueFromDescriptor = nil
	if _, err := NewQueue(queue.Config{Driver: backend.Driver()}, nil, descriptor); err == nil || !strings.Contains(err.Error(), "descriptor hook is not registered") {
		t.Fatalf("unregistered hook=%v", err)
	}
	runtimehook.BuildQueueFromDescriptor = func(any, any, runtimehook.Descriptor, []any) (any, error) { return 42, nil }
	if _, err := NewQueue(queue.Config{Driver: backend.Driver()}, nil, descriptor); err == nil || !strings.Contains(err.Error(), "returned int") {
		t.Fatalf("invalid hook return=%v", err)
	}
	if _, err := NewQueue(queue.Config{}, nil, runtimehook.Descriptor{}); err == nil {
		t.Fatal("nil backend accepted")
	}
}
