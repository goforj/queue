//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// New creates a queue based on Config.Driver.

	// Example: new queue from config
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
	_ = q.Enqueue(
		context.Background(),
		queue.NewTask("emails:send").
			Payload(EmailPayload{ID: 1}).
			OnQueue("default"),
	)
}
