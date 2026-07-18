package queue_test

import (
	"reflect"
	"testing"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

const (
	queuePackagePath = "github.com/goforj/queue"
	busPackagePath   = "github.com/goforj/queue/bus"
)

// TestPublicWorkflowTypesAreOwnedByQueue pins queue as the physical owner of the canonical workflow model.
func TestPublicWorkflowTypesAreOwnedByQueue(t *testing.T) {
	t.Parallel()

	types := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Message", typeOf: reflectedType[queue.Message]()},
		{name: "DispatchResult", typeOf: reflectedType[queue.DispatchResult]()},
		{name: "StoredJobOptions", typeOf: reflectedType[queue.StoredJobOptions]()},
		{name: "StoredJob", typeOf: reflectedType[queue.StoredJob]()},
		{name: "ChainNode", typeOf: reflectedType[queue.ChainNode]()},
		{name: "ChainRecord", typeOf: reflectedType[queue.ChainRecord]()},
		{name: "ChainState", typeOf: reflectedType[queue.ChainState]()},
		{name: "BatchJob", typeOf: reflectedType[queue.BatchJob]()},
		{name: "BatchJobOutcome", typeOf: reflectedType[queue.BatchJobOutcome]()},
		{name: "BatchRecord", typeOf: reflectedType[queue.BatchRecord]()},
		{name: "BatchState", typeOf: reflectedType[queue.BatchState]()},
		{name: "SQLStoreConfig", typeOf: reflectedType[queue.SQLStoreConfig]()},
		{name: "MiddlewareFunc", typeOf: reflectedType[queue.MiddlewareFunc]()},
		{name: "RetryPolicy", typeOf: reflectedType[queue.RetryPolicy]()},
		{name: "SkipWhen", typeOf: reflectedType[queue.SkipWhen]()},
		{name: "FailOnError", typeOf: reflectedType[queue.FailOnError]()},
		{name: "RateLimit", typeOf: reflectedType[queue.RateLimit]()},
		{name: "WithoutOverlapping", typeOf: reflectedType[queue.WithoutOverlapping]()},
		{name: "WorkflowStore", typeOf: reflectedType[queue.WorkflowStore]()},
		{name: "WorkflowOutcomeStore", typeOf: reflectedType[queue.WorkflowOutcomeStore]()},
	}

	for _, contract := range types {
		if got := contract.typeOf.PkgPath(); got != queuePackagePath {
			t.Errorf("queue.%s package path = %q, want %q", contract.name, got, queuePackagePath)
		}
	}
}

// TestBusCompatibleAliasesResolveToQueue pins the deprecated facade to the canonical queue identities.
func TestBusCompatibleAliasesResolveToQueue(t *testing.T) {
	t.Parallel()

	aliases := []struct {
		name      string
		busType   reflect.Type
		queueType reflect.Type
	}{
		{name: "Context", busType: reflectedType[bus.Context](), queueType: reflectedType[queue.Message]()},
		{name: "JobOptions", busType: reflectedType[bus.JobOptions](), queueType: reflectedType[queue.StoredJobOptions]()},
		{name: "DispatchResult", busType: reflectedType[bus.DispatchResult](), queueType: reflectedType[queue.DispatchResult]()},
		{name: "StoredJob", busType: reflectedType[bus.StoredJob](), queueType: reflectedType[queue.StoredJob]()},
		{name: "ChainNode", busType: reflectedType[bus.ChainNode](), queueType: reflectedType[queue.ChainNode]()},
		{name: "ChainRecord", busType: reflectedType[bus.ChainRecord](), queueType: reflectedType[queue.ChainRecord]()},
		{name: "ChainState", busType: reflectedType[bus.ChainState](), queueType: reflectedType[queue.ChainState]()},
		{name: "BatchJob", busType: reflectedType[bus.BatchJob](), queueType: reflectedType[queue.BatchJob]()},
		{name: "BatchJobOutcome", busType: reflectedType[bus.BatchJobOutcome](), queueType: reflectedType[queue.BatchJobOutcome]()},
		{name: "BatchRecord", busType: reflectedType[bus.BatchRecord](), queueType: reflectedType[queue.BatchRecord]()},
		{name: "BatchState", busType: reflectedType[bus.BatchState](), queueType: reflectedType[queue.BatchState]()},
		{name: "Store", busType: reflectedType[bus.Store](), queueType: reflectedType[queue.WorkflowStore]()},
		{name: "WorkflowOutcomeStore", busType: reflectedType[bus.WorkflowOutcomeStore](), queueType: reflectedType[queue.WorkflowOutcomeStore]()},
		{name: "SQLStoreConfig", busType: reflectedType[bus.SQLStoreConfig](), queueType: reflectedType[queue.SQLStoreConfig]()},
		{name: "Next", busType: reflectedType[bus.Next](), queueType: reflectedType[queue.Next]()},
		{name: "Middleware", busType: reflectedType[bus.Middleware](), queueType: reflectedType[queue.Middleware]()},
		{name: "MiddlewareFunc", busType: reflectedType[bus.MiddlewareFunc](), queueType: reflectedType[queue.MiddlewareFunc]()},
		{name: "RetryPolicy", busType: reflectedType[bus.RetryPolicy](), queueType: reflectedType[queue.RetryPolicy]()},
		{name: "SkipWhen", busType: reflectedType[bus.SkipWhen](), queueType: reflectedType[queue.SkipWhen]()},
		{name: "FailOnError", busType: reflectedType[bus.FailOnError](), queueType: reflectedType[queue.FailOnError]()},
		{name: "RateLimiter", busType: reflectedType[bus.RateLimiter](), queueType: reflectedType[queue.RateLimiter]()},
		{name: "RateLimit", busType: reflectedType[bus.RateLimit](), queueType: reflectedType[queue.RateLimit]()},
		{name: "Lock", busType: reflectedType[bus.Lock](), queueType: reflectedType[queue.Lock]()},
		{name: "Locker", busType: reflectedType[bus.Locker](), queueType: reflectedType[queue.Locker]()},
		{name: "WithoutOverlapping", busType: reflectedType[bus.WithoutOverlapping](), queueType: reflectedType[queue.WithoutOverlapping]()},
	}

	for _, contract := range aliases {
		if contract.busType != contract.queueType {
			t.Errorf("bus.%s type = %v, want queue identity %v", contract.name, contract.busType, contract.queueType)
			continue
		}
		if got := contract.busType.PkgPath(); got != queuePackagePath {
			t.Errorf("bus.%s package path = %q, want canonical queue path %q", contract.name, got, queuePackagePath)
		}
	}
}

// TestLegacyBusTypesRemainOwnedByBus pins the intentionally distinct compatibility contracts to the bus package.
func TestLegacyBusTypesRemainOwnedByBus(t *testing.T) {
	t.Parallel()

	contracts := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Bus", typeOf: reflectedType[bus.Bus]()},
		{name: "BatchSpec", typeOf: reflectedType[bus.BatchSpec]()},
		{name: "Fake", typeOf: reflectedType[bus.Fake]()},
		{name: "Handler", typeOf: reflectedType[bus.Handler]()},
		{name: "Observer", typeOf: reflectedType[bus.Observer]()},
		{name: "ObserverFunc", typeOf: reflectedType[bus.ObserverFunc]()},
		{name: "Option", typeOf: reflectedType[bus.Option]()},
	}

	for _, contract := range contracts {
		if got := contract.typeOf.PkgPath(); got != busPackagePath {
			t.Errorf("bus.%s package path = %q, want %q", contract.name, got, busPackagePath)
		}
	}

	types := []struct {
		name      string
		busType   reflect.Type
		queueType reflect.Type
	}{
		{name: "Job", busType: reflectedType[bus.Job](), queueType: reflectedType[queue.Job]()},
		{name: "Event", busType: reflectedType[bus.Event](), queueType: reflectedType[queue.Event]()},
		{name: "EventKind", busType: reflectedType[bus.EventKind](), queueType: reflectedType[queue.EventKind]()},
		{name: "ChainBuilder", busType: reflectedType[bus.ChainBuilder](), queueType: reflectedType[queue.ChainBuilder]()},
		{name: "BatchBuilder", busType: reflectedType[bus.BatchBuilder](), queueType: reflectedType[queue.BatchBuilder]()},
	}

	for _, contract := range types {
		if got := contract.busType.PkgPath(); got != busPackagePath {
			t.Errorf("bus.%s package path = %q, want %q", contract.name, got, busPackagePath)
		}
		if contract.busType == contract.queueType {
			t.Errorf("bus.%s unexpectedly shares queue identity %v", contract.name, contract.queueType)
		}
	}
}

// reflectedType returns the reflection identity for T, including interface types.
func reflectedType[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}
