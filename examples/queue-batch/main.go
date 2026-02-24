//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Batch creates a batch builder for fan-out workflow execution.

	// Example: batch
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
	_, _ = q.Batch(
		queue.NewJob("emails:send").Payload(map[string]any{"id": 1}),
		queue.NewJob("emails:send").Payload(map[string]any{"id": 2}),
	).Name("send-emails").OnQueue("default").Dispatch(context.Background())
}
