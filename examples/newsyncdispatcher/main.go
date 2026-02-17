//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewSyncDispatcher creates a synchronous in-process dispatcher.

	// Example: new sync dispatcher
	dispatcher := queue.NewSyncDispatcher()
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
}
