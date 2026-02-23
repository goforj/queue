//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// New creates the high-level Queue API based on Config.Driver.

	// Example: create a queue and dispatch a workflow-capable job
	q, err := queue.New(queue.Config{Driver: queue.DriverWorkerpool})
	if err != nil {
		return
	}
	type EmailPayload struct {
		ID int `json:"id"`
	}
	q.Register("emails:send", func(ctx context.Context, j queue.Context) error {
		var payload EmailPayload
		if err := j.Bind(&payload); err != nil {
			return err
		}
		_ = payload
		return nil
	})
	_ = q.Workers(1).StartWorkers(context.Background())
	defer q.Shutdown(context.Background())
	_, _ = q.Dispatch(
		context.Background(),
		queue.NewJob("emails:send").
			Payload(EmailPayload{ID: 1}).
			OnQueue("default"),
	)
}
