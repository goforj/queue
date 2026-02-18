//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewDispatcher creates a dispatcher based on Config.Driver.

	// Example: new dispatcher from config
	dispatcher, err := queue.NewDispatcher(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
}
