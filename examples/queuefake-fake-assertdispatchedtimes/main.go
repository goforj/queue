//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertDispatchedTimes fails when jobType dispatch count does not match expected.

	// Example: assert queue dispatch count by type
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("emails:send"))
	_ = q.Dispatch(queue.NewJob("emails:send"))
	f.AssertDispatchedTimes(nil, "emails:send", 2)
}
