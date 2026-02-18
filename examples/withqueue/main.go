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
	dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithQueue("critical"))
	_ = err
}
