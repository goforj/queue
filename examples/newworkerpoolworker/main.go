//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewWorkerpoolWorker creates an in-process asynchronous workerpool worker.

	// Example: new workerpool worker
	worker := queue.NewWorkerpoolWorker(queue.WorkerpoolConfig{Workers: 2, Buffer: 16})
	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = worker.Start()
	_ = worker.Shutdown()
}
