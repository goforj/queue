//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// CountOn returns how many times a job type was dispatched on a queue.

	// Example: count by queue and job type
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("critical"))
	_ = f.CountOn("critical", "emails:send")
}
