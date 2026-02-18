//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Register adds a task handler to the local dispatcher.

	// Example: local register
	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
}
