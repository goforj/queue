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
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	type EmailPayload struct {
		ID int `json:"id"`
	}
	q.Register("emails:send", func(ctx context.Context, task queue.Job) error {
		var payload EmailPayload
		if err := task.Bind(&payload); err != nil {
			return err
		}
		_ = payload
		return nil
	})
	_ = q.Workers(1).StartWorkers(context.Background())
	defer q.Shutdown(context.Background())
	_ = q.DispatchCtx(
		context.Background(),
		queue.NewJob("emails:send").
			Payload(EmailPayload{ID: 1}).
			OnQueue("default"),
	)
}
