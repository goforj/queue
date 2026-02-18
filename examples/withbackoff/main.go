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
	dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
	err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithBackoff(2*time.Second))
	_ = err
}
