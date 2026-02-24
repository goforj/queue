//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Run starts worker processing, blocks until ctx is canceled, then gracefully shuts down.

	// Example: run until canceled
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_ = q.Run(ctx)
}
