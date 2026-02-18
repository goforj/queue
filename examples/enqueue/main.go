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
	dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	_ = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithDelay(10*time.Millisecond))
}
