//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// Queue returns the queue fake to inject into code under test.

	// Example: queue fake
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
}
