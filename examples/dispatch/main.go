//go:build ignore
// +build ignore

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
	// Dispatch records a typed job payload in-memory using the fake default queue.

	// Example: dispatch typed task
	var q queue.Queue
	err := q.Dispatch(
		queue.NewTask("emails:send").
			Payload(map[string]any{"id": 1}).
			OnQueue("default"),
	)
	_ = err
}

func example2() {
	// Example: dispatch to fake queue
	fake := queue.NewFake()
	err := fake.Dispatch(queue.NewTask("emails:send").OnQueue("default"))
	_ = err
}

func example3() {
	// Example: local dispatch
	q, err := queue.NewSync()
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

