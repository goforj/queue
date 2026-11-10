// Package contextpropagation shares private preparation markers between physical queues and the workflow bus.
package contextpropagation

import "context"

// Propagator describes the transport-neutral contract without coupling this internal package to either caller.
type Propagator interface {
	// Inject snapshots selected correlation fields for the next delivery.
	Inject(context.Context) map[string]string
	// Extract restores selected correlation onto the receiver's lifetime.
	Extract(context.Context, map[string]string) context.Context
}

// Prepared delegates injection after the physical worker has already restored the receiving context.
type Prepared struct{ Propagator }

// Extract preserves decorators' derived contexts instead of replacing them with the transported parent again.
func (Prepared) Extract(ctx context.Context, _ map[string]string) context.Context { return ctx }
