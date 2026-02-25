//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertWorkflowDispatchedOn fails when jobType was not workflow-dispatched on queueName.

	// Example: assert workflow dispatch on queue
	f := queuefake.New()
	_, _ = f.Workflow().Chain(bus.NewJob("a", nil)).OnQueue("critical").Dispatch(nil)
	f.AssertWorkflowDispatchedOn(nil, "critical", "a")
}
