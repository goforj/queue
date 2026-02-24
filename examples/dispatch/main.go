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
	example1()
	example2()
	example3()
}

func example1() {
	// Dispatch submits a typed job payload using the default queue.

	// Example: dispatch to fake queue
	fake := queue.NewFake()
	err := fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
	_ = err
}

func example2() {
	// Example: local dispatch
	q, err := queue.NewSync()
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
	job := queue.NewJob("emails:send").
		Payload(EmailPayload{ID: 1}).
		OnQueue("default").
		Delay(10 * time.Millisecond)
	_, _ = q.Dispatch(job)
}

func example3() {
	// Example: dispatch
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, j queue.Context) error { return nil })
	job := queue.NewJob("emails:send").Payload(map[string]any{"id": 1}).OnQueue("default")
	_, _ = q.Dispatch(job)
}

