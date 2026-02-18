//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// WithTimeout sets per-task execution timeout.

	// Example: with timeout
	dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithTimeout(15*time.Second))
	_ = err
}
