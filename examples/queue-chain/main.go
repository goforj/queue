//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Chain creates a chain builder for sequential workflow execution.

	// Example: chain
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	q.Register("first", func(ctx context.Context, m queue.Message) error { return nil })
	q.Register("second", func(ctx context.Context, m queue.Message) error { return nil })
	_, _ = q.Chain(
		queue.NewJob("first"),
		queue.NewJob("second"),
	).OnQueue("default").Dispatch(context.Background())
}
