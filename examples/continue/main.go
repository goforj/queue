//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Continue snapshots a queue-owned chain or batch without changing existing builder interfaces.
	// Unsupported custom builders and mixed closure callbacks fail when Dispatch is called.

	// Example: named durable completion
	q, err := queue.NewSync(queue.WithResourceID("reports"))
	if err != nil {
		return
	}
	q.Register("reports:build", func(context.Context, queue.Message) error { return nil })
	q.Register("reports:complete", func(_ context.Context, m queue.Message) error {
		invocation, _ := m.Continuation()
		_ = invocation.ID // Stable across retry, suitable for application idempotency.
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		return
	}
	defer q.Shutdown(context.Background())
	id, err := queue.Continue(q.Chain(queue.NewJob("reports:build"))).
		OnCompletion(queue.NewJob("reports:complete")).Dispatch(context.Background())
	_ = id
	_ = err
}
