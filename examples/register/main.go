//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	example1()
	example2()
	example3()
	example4()
}

func example1() {
	// Register associates a handler with a job type.

	// Example: register a handler
	var q queue.QueueRuntime
	q.Register("emails:send", func(context.Context, queue.Job) error { return nil })
}

func example2() {
	// Example: local register
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
}

func example3() {
	// Example: register no-op on fake
	fake := queue.NewFake()
	fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
}

func example4() {
	// Example: register
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
}

