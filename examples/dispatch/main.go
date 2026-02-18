//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Dispatch schedules or executes a task using the local driver.

	// Example: local dispatch
	queuer, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	queuer.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	_ = queuer.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithDelay(10*time.Millisecond))
}
