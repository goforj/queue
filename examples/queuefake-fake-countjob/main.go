//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// CountJob returns how many times a job type was dispatched.

	// Example: count by job type
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("emails:send"))
	_ = q.Dispatch(queue.NewJob("emails:send"))
	_ = f.CountJob("emails:send")
}
