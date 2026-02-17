//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewWorkerpoolDispatcher creates an in-memory asynchronous workerpool dispatcher.

	// Example: new workerpool dispatcher
	dispatcher := queue.NewWorkerpoolDispatcher(queue.WorkerpoolConfig{Workers: 2, Buffer: 16})
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = dispatcher.Start(context.Background())
	_ = dispatcher.Shutdown(context.Background())
}
