//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertDispatchedOn fails when jobType was not dispatched on queueName.

	// Example: assert queue dispatch on queue
	f := queuefake.New()
	_ = f.Queue().Dispatch(queue.NewJob("emails:send").OnQueue("critical"))
	f.AssertDispatchedOn(nil, "critical", "emails:send")
}
