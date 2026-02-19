//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewQueueWithDefaults creates a queue runtime and sets the default queue name.

	// Example: new queue with default queue
	q, err := queue.NewQueueWithDefaults("critical", queue.Config{
		Driver: queue.DriverSync,
	})
	if err != nil {
		return
	}
	type EmailPayload struct {
		ID int `json:"id"`
	}
	q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		var payload EmailPayload
		if err := task.Bind(&payload); err != nil {
			return err
		}
		_ = payload
		return nil
	})
	_ = q.StartWorkers(context.Background())
	defer q.Shutdown(context.Background())
	_ = q.Dispatch(queue.NewTask("emails:send").Payload(EmailPayload{ID: 1}))
}
