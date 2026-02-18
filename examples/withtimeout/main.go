//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// WithTimeout sets per-task execution timeout.

	// Example: with timeout
	dispatcher, err := queue.NewDispatcher(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	ctx := context.Background()
	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithTimeout(15*time.Second))
	_ = err
}
