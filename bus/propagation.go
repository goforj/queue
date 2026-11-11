package bus

import (
	"context"
	"encoding/json"
	"github.com/goforj/queue/internal/contextpropagation"
)

// ContextPropagator transfers explicitly selected context metadata across physical job deliveries.
// Implementations must be safe for concurrent use and retain the receiver's cancellation and deadline.
// @group Options
type ContextPropagator interface {
	// Inject returns metadata to snapshot into an optional field of the existing delivery envelope.
	Inject(context.Context) map[string]string
	// Extract restores that metadata onto the receiving worker's context.
	Extract(context.Context, map[string]string) context.Context
}

// WithContextPropagator installs optional propagation without adding a tracing SDK dependency.
// @group Options
func WithContextPropagator(propagator ContextPropagator) Option {
	return func(runtime *runtime) { runtime.propagator = propagator }
}

// dispatchContext snapshots a bounded carrier independently from the application's exact payload.
func (r *runtime) dispatchContext(ctx context.Context) json.RawMessage {
	if r.propagator == nil {
		return nil
	}
	headers := r.propagator.Inject(ctx)
	if len(headers) == 0 || len(headers) > 32 {
		return nil
	}
	size := 0
	for key, value := range headers {
		size += len(key) + len(value)
		if size > 8192 {
			return nil
		}
	}
	// String maps cannot contain unsupported JSON values.
	encoded, _ := json.Marshal(headers)
	if len(encoded) > 8192 {
		return nil
	}
	return encoded
}

// deliveryContext treats malformed correlation as absent while retaining the worker's execution lifetime.
func (r *runtime) deliveryContext(ctx context.Context, encoded json.RawMessage) context.Context {
	if r.propagator == nil || len(encoded) == 0 || len(encoded) > 8192 {
		return ctx
	}
	// Physical queues already validated this carrier before decorators and observations.
	if _, prepared := r.propagator.(contextpropagation.Prepared); prepared {
		return ctx
	}
	var headers map[string]string
	if json.Unmarshal(encoded, &headers) != nil || len(headers) == 0 || len(headers) > 32 {
		return ctx
	}
	return r.propagator.Extract(ctx, headers)
}
