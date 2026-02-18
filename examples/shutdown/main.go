//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Shutdown drains delayed and active local workerpool tasks.

	// Example: local shutdown
	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{
		Driver: queue.DriverWorkerpool,
	})
	if err != nil {
		return
	}
	_ = dispatcher.Start(context.Background())
	_ = dispatcher.Shutdown(context.Background())
}
