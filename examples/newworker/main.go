//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewWorker creates a worker based on Config.Driver.

	// Example: new sync worker
	worker, err := queue.NewWorker(queue.Config{
		Driver: queue.DriverSync,
	})
	if err != nil {
		return
	}
	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = worker.Start()
	_ = worker.Shutdown()
}
