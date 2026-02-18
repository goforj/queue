//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewQueue creates a dispatcher based on QueueConfig.Driver.

	// Example: new dispatcher from config
	dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`))
}
