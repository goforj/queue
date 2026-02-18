//go:build ignore
// +build ignore

package main

import (
	"context"
	"time"

	"github.com/goforj/queue"
)

func main() {
	dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}

	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})

	_ = dispatcher.Dispatch(
		"emails:send",
		[]byte(`{"id":123}`),
		queue.WithQueue("critical"),
		queue.WithTimeout(20*time.Second),
		queue.WithMaxRetry(3),
		queue.WithBackoff(2*time.Second),
		queue.WithDelay(250*time.Millisecond),
		queue.WithUnique(30*time.Second),
	)
}
