//go:build ignore
// +build ignore

package main

import (
	"context"
	"time"

	"github.com/goforj/queue"
)

func main() {
	dispatcher, err := queue.NewDispatcher(queue.Config{
		Driver:        queue.DriverWorkerpool,
		Workers:       4,
		QueueCapacity: 64,
		TaskTimeout:   30 * time.Second,
	})
	if err != nil {
		return
	}

	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})

	ctx := context.Background()
	_ = dispatcher.Start(ctx)
	defer dispatcher.Shutdown(ctx)

	_ = dispatcher.Enqueue(
		ctx,
		queue.Task{Type: "emails:send", Payload: []byte(`{"id":456}`)},
		queue.WithQueue("default"),
		queue.WithTimeout(15*time.Second),
		queue.WithMaxRetry(5),
		queue.WithBackoff(750*time.Millisecond),
		queue.WithDelay(500*time.Millisecond),
		queue.WithUnique(20*time.Second),
	)
}
