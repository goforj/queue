//go:build ignore
// +build ignore

package main

import (
	"context"
	"time"

	"github.com/goforj/queue"
)

func main() {
	dispatcher, err := queue.NewQueue(queue.QueueConfig{
		Driver:         queue.DriverDatabase,
		DatabaseDriver: "sqlite",
		DatabaseDSN:    "file:queue.db?_busy_timeout=5000",
		DefaultQueue:   "default",
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

	_ = dispatcher.Dispatch(
		"emails:send",
		[]byte(`{"id":789}`),
		queue.WithQueue("critical"),
		queue.WithTimeout(10*time.Second),
		queue.WithMaxRetry(4),
		queue.WithBackoff(500*time.Millisecond),
		queue.WithDelay(300*time.Millisecond),
		queue.WithUnique(45*time.Second),
	)
}
