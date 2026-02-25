//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertWorkflowDispatched fails when jobType was not workflow-dispatched.

	// Example: assert workflow dispatch by type
	f := queuefake.New()
	_, _ = f.Workflow().Chain(bus.NewJob("a", nil)).Dispatch(nil)
	f.AssertWorkflowDispatched(nil, "a")
}
