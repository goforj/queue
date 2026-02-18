//go:build ignore
// +build ignore

package main

import (
	"context"
	"time"

	"github.com/goforj/queue"
)

func main() {
	worker, err := queue.NewWorker(queue.Config{
		Driver:        queue.DriverWorkerpool,
		Workers:       4,
		QueueCapacity: 64,
		TaskTimeout:   20 * time.Second,
	})
	if err != nil {
		return
	}

	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})

	_ = worker.Start()
	defer worker.Shutdown()
}
