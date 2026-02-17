//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewDatabaseDispatcher creates a durable SQL-backed dispatcher.

	// Example: new database dispatcher
	dispatcher, err := queue.NewDatabaseDispatcher(queue.DatabaseConfig{
		DriverName: "sqlite",
		DSN:        "file:queue.db?_busy_timeout=5000",
		Workers:    1,
	})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = dispatcher.Start(context.Background())
	_ = dispatcher.Shutdown(context.Background())
}
