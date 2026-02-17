//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// WithUnique deduplicates by task type and payload for a TTL window.

	// Example: with unique
	dispatcher := queue.NewSyncDispatcher()
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	ctx := context.Background()
	err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send", Payload: []byte(`{"id":1}`)}, queue.WithUnique(30*time.Second))
	_ = err
}
