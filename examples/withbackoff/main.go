//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// WithBackoff sets delay between retry attempts.

	// Example: with backoff
	dispatcher := queue.NewSyncDispatcher()
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	ctx := context.Background()
	err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithBackoff(2*time.Second))
	_ = err
}
