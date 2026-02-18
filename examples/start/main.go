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
	dispatcher, err := queue.NewDispatcher(queue.Config{
		Driver:     queue.DriverWorkerpool,
		Workerpool: queue.WorkerpoolConfig{Workers: 1, Buffer: 4},
	})
	if err != nil {
		return
	}
	_ = dispatcher.Start(context.Background())
}
