//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Pause pauses consumption for a queue when supported by the underlying driver.

	// Example: pause queue
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	if queue.SupportsPause(q) {
		_ = q.Pause(context.Background(), "default")
	}
}
