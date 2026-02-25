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
	// See the README "Queue Backends" table for Pause/Resume support and
	// docs/backend-guarantees.md (Capability Matrix) for broader backend differences.

	// Example: pause queue
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	if queue.SupportsPause(q) {
		_ = q.Pause(context.Background(), "default")
	}
}
