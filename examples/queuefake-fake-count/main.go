//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// Count returns the total number of recorded dispatches.

	// Example: count total dispatches
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("a"))
	_ = q.Dispatch(queue.NewJob("b"))
	_ = f.Count()
}
