//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Dispatch enqueues a high-level job using its application type and exact
	// payload bytes together with the queue's bound context.

	// Example: dispatch
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
	if err := q.StartWorkers(context.Background()); err != nil {
		return
	}
	defer q.Shutdown(context.Background())
	job := queue.NewJob("emails:send").Payload(map[string]any{"id": 1}).OnQueue("default")
	_, _ = q.Dispatch(job)
}
