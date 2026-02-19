//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Enqueue schedules or executes a task using the local driver.

	// Example: local enqueue
	q, err := queue.New(queue.Config{Driver: queue.DriverSync})
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
	task := queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1}).
		OnQueue("default").
		Delay(10 * time.Millisecond)
	_ = q.DispatchCtx(context.Background(), task)
}
