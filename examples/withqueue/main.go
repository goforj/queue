//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// WithQueue routes a task to a named queue.

	// Example: with queue
	dispatcher := queue.NewSyncDispatcher()
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	ctx := context.Background()
	err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithQueue("critical"))
	_ = err
}
