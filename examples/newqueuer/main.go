//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewQueue creates a queuer based on QueueConfig.Driver.

	// Example: new queuer from config
	queuer, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	queuer.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})
	_ = queuer.Dispatch("emails:send", []byte(`{"id":1}`))
}
