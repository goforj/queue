//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Enqueue schedules or executes a task using the local driver.

	// Example: local enqueue
	dispatcher := queue.NewSyncDispatcher()
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"}, queue.WithDelay(10*time.Millisecond))
}
