//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Register adds a task handler to the local queue runtime.

	// Example: local register
	q, err := queue.New(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
}
