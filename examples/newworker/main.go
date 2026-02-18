//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewWorker creates a worker based on WorkerConfig.Driver.

	// Example: new sync worker
	worker, err := queue.NewWorker(queue.WorkerConfig{
		Driver: queue.DriverSync,
	})
	if err != nil {
		return
	}
	type EmailPayload struct {
		ID int `json:"id"`
	}
	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		var payload EmailPayload
		if err := task.Bind(&payload); err != nil {
			return err
		}
		_ = payload
		return nil
	})
	_ = worker.Start()
	_ = worker.Shutdown()
}
