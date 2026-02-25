//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// Reset clears recorded dispatches.

	// Example: reset recorded dispatches
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("emails:send"))
	f.Reset()
	f.AssertNothingDispatched(t)
}
