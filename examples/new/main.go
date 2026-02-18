//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// New creates a queue based on Config.Driver.

	// Example: new queue from config
	q, err := queue.New(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = q.Enqueue(context.Background(), queue.NewTask("emails:send").Payload([]byte(`{"id":1}`)))
}
