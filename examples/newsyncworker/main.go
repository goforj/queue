//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewSyncWorker creates a synchronous in-process worker.

	// Example: new sync worker
	worker := queue.NewSyncWorker()
	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = worker.Start()
	_ = worker.Shutdown()
}
