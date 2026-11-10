package queue

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/goforj/queue/bus"
)

// ContextPropagator transfers selected context metadata without serializing application context values or lifetimes.
// @group Queue
type ContextPropagator = bus.ContextPropagator

// WithContextPropagator installs optional transport-neutral context propagation while preserving the application job payload.
// @group Queue
func WithContextPropagator(propagator ContextPropagator) Option {
	return func(options *runtimeOptions) {
		options.contextPropagator = propagator
	}
}

// preparedContextPropagator leaves extraction to the physical worker before decorators and lifecycle observers run.
type preparedContextPropagator struct{ ContextPropagator }

// Extract retains the consumer context already derived from the transported parent by the worker boundary.
func (preparedContextPropagator) Extract(ctx context.Context, _ map[string]string) context.Context {
	return ctx
}

// queueContextPropagatorSetter keeps physical preparation internal while allowing other runtime implementations to use bus extraction.
type queueContextPropagatorSetter interface {
	setContextPropagator(ContextPropagator)
}

// setContextPropagator retains the application's real propagator at the physical execution boundary.
func (q *nativeQueueRuntime) setContextPropagator(propagator ContextPropagator) {
	q.common.contextPropagator = propagator
}

// setContextPropagator retains the application's real propagator for external worker preparation.
func (q *externalQueueRuntime) setContextPropagator(propagator ContextPropagator) {
	q.common.contextPropagator = propagator
}

// prepareHandlerContext restores bounded metadata only from known workflow envelopes without adopting publisher lifetime.
func (q *queueCommon) prepareHandlerContext(ctx context.Context, job Job) context.Context {
	if q.contextPropagator == nil {
		return ctx
	}
	switch job.Type {
	case "bus:job", "bus:chain:node", "bus:batch:job", "bus:callback":
	default:
		return ctx
	}
	// Generated envelopes omit the canonical context field without a carrier, so avoid decoding that hot path twice.
	if !bytes.Contains(job.payload, []byte(`"context"`)) {
		return ctx
	}
	var envelope struct {
		Context json.RawMessage `json:"context"`
	}
	if json.Unmarshal(job.payload, &envelope) != nil || len(envelope.Context) == 0 || len(envelope.Context) > 8192 {
		return ctx
	}
	var headers map[string]string
	if json.Unmarshal(envelope.Context, &headers) != nil || len(headers) == 0 || len(headers) > 32 {
		return ctx
	}
	return q.contextPropagator.Extract(ctx, headers)
}
