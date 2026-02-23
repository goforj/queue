//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"time"
)

func main() {
	// WithClock overrides the workflow runtime clock.

	// Example: workflow clock
	q, err := queue.New(
		queue.Config{Driver: queue.DriverSync},
		queue.WithClock(func() time.Time { return time.Unix(0, 0) }),
	)
	if err != nil {
		return
	}
	_ = q
}
