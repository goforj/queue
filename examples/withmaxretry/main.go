//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// WithMaxRetry sets maximum retry attempts.

	// Example: with max retry
	dispatcher := queue.NewSyncDispatcher()
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	ctx := context.Background()
	err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithMaxRetry(3))
	_ = err
}
