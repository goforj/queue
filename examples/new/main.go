//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
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
	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
		var payload EmailPayload
		if err := m.Bind(&payload); err != nil {
			return err
		}
		_ = payload
		return nil
	})
	_ = q.WithWorkers(1).StartWorkers(context.Background()) // optional; default: runtime.NumCPU() (min 1)
	defer q.Shutdown(context.Background())
	_, _ = q.Dispatch(
		queue.NewJob("emails:send").
			Payload(EmailPayload{ID: 1}).
			OnQueue("default"),
	)
}
