package queue_test

import (
	"reflect"
	"testing"

	"github.com/goforj/queue"
)

const (
	queuePackagePath = "github.com/goforj/queue"
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

// reflectedType returns the reflection identity for T, including interface types.
func reflectedType[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}
