package queue

import "github.com/goforj/queue/bus"

// ContextPropagator transfers selected context metadata without serializing application context values or lifetimes.
// @group Queue
type ContextPropagator = bus.ContextPropagator

// WithContextPropagator installs optional transport-neutral context propagation while preserving the application job payload.
// @group Queue
func WithContextPropagator(propagator ContextPropagator) Option {
	return func(options *runtimeOptions) {
		options.busOpts = append(options.busOpts, bus.WithContextPropagator(propagator))
	}
}
