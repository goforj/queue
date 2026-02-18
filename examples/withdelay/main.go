//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// WithDelay schedules processing after a delay.

	// Example: with delay
	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	ctx := context.Background()
	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithDelay(10*time.Second))
	_ = err
}
