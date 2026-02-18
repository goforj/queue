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
	dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithMaxRetry(3))
	_ = err
}
