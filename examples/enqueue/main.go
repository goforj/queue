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
	q, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	task := queue.NewTask("emails:send").Payload([]byte(`{"id":1}`)).Delay(10 * time.Millisecond)
	_ = q.Enqueue(context.Background(), task)
}
