//go:build ignore
// +build ignore

package main

import (
	"context"
	"time"

	"github.com/goforj/queue"
)

func main() {
	worker, err := queue.NewWorker(queue.WorkerConfig{
		Driver:         queue.DriverDatabase,
		DatabaseDriver: "sqlite",
		DatabaseDSN:    "file:queue-worker.db?_busy_timeout=5000",
		Workers:        2,
		PollInterval:   50 * time.Millisecond,
		AutoMigrate:    true,
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
