//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// NewQueue creates the low-level queue runtime (driver-facing API) based on Config.Driver.
	// Use this only for driver-focused/advanced runtime access; application code should prefer New.

	// Example: new queue runtime with default queue
	q, err := queue.NewQueue(queue.Config{
		Driver:       queue.DriverSync,
		DefaultQueue: "critical",
	})
	if err != nil {
		return
	}
	type EmailPayload struct {
		ID int `json:"id"`
	}
	q.Register("emails:send", func(ctx context.Context, job queue.Job) error {
		var payload EmailPayload
		if err := job.Bind(&payload); err != nil {
			return err
		}
		_ = payload
		return nil
	})
	_ = q.StartWorkers(context.Background())
	defer q.Shutdown(context.Background())
	_ = q.Dispatch(queue.NewJob("emails:send").Payload(EmailPayload{ID: 1}))
}
