//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Register is a no-op for fake queue.

	// Example: local register
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
}
