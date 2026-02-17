//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewDatabaseWorker creates a durable SQL-backed worker.

	// Example: new database worker
	worker, err := queue.NewDatabaseWorker(queue.DatabaseConfig{
		DriverName: "sqlite",
		DSN:        "file:queue-worker.db?_busy_timeout=5000",
		Workers:    1,
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
