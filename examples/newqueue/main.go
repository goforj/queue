//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewQueue creates a queue based on QueueConfig.Driver.

	// Example: new queue from config
	q, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = q.Enqueue(context.Background(), queue.NewTask("emails:send").Payload([]byte(`{"id":1}`)))
}
