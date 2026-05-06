//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Dispatch enqueues a high-level job using the queue's bound context.

	// Example: dispatch
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
	job := queue.NewJob("emails:send").Payload(map[string]any{"id": 1}).OnQueue("default")
	_, _ = q.Dispatch(job)
}
