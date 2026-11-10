package queue

import (
	"context"
	"encoding/json"
)

// ContextPropagator transfers explicitly selected context values across queue deliveries.
// Implementations must be safe for concurrent use. The queue never serializes context.Context itself.
// @group Queue
type ContextPropagator interface {
	// Inject returns message metadata to snapshot at dispatch. An empty map leaves delivery unchanged.
	Inject(context.Context) map[string]string
	// Extract restores message metadata onto the worker's context, preserving its cancellation and deadline.
	Extract(context.Context, map[string]string) context.Context
}

// WithContextPropagator installs optional transport-neutral context propagation without changing job payloads.
// Metadata is limited to 8 KiB and 32 entries. Invalid or oversized metadata is ignored so it cannot prevent delivery.
// @group Queue
func WithContextPropagator(propagator ContextPropagator) Option {
	return func(options *runtimeOptions) { options.contextPropagator = propagator }
}

// attachContextPropagator configures the shared physical delivery boundary before worker registration.
func attachContextPropagator(runtime queueRuntime, propagator ContextPropagator) {
	switch runtime := runtime.(type) {
	case *nativeQueueRuntime:
		runtime.common.contextPropagator = propagator
	case *externalQueueRuntime:
		runtime.common.contextPropagator = propagator
	}
}

// dispatchContext snapshots only explicit carrier fields, leaving ordinary jobs and their identities intact.
func (q *queueCommon) dispatchContext(ctx context.Context, job Job) Job {
	if q.contextPropagator == nil {
		return job
	}
	headers := q.contextPropagator.Inject(ctx)
	if len(headers) == 0 || len(headers) > 32 {
		return job
	}
	size := 0
	for key, value := range headers {
		size += len(key) + len(value)
		if size > 8192 {
			return job
		}
	}
	// A string map has no unsupported JSON values, so encoding cannot fail.
	encoded, _ := json.Marshal(headers)
	if len(encoded) > 8192 {
		return job
	}
	metadata := DriverMetadata(job)
	metadata.SchemaVersion = DriverJobMetadataVersion
	metadata.Context = string(encoded)
	return DriverWithMetadata(job, metadata)
}

// deliveryContext restores one delivery without retaining the producer's values, cancellation, or deadline.
func (q *queueCommon) deliveryContext(ctx context.Context, job Job) context.Context {
	if q.contextPropagator == nil {
		return ctx
	}
	encoded := DriverMetadata(job).Context
	if encoded == "" || len(encoded) > 8192 {
		return ctx
	}
	var headers map[string]string
	if json.Unmarshal([]byte(encoded), &headers) != nil || len(headers) == 0 || len(headers) > 32 {
		return ctx
	}
	return q.contextPropagator.Extract(ctx, headers)
}
