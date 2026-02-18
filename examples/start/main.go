//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Start initializes worker goroutines for workerpool mode.

	// Example: local start
	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{
		Driver: queue.DriverWorkerpool,
	})
	if err != nil {
		return
	}
	_ = dispatcher.Start(context.Background())
}
