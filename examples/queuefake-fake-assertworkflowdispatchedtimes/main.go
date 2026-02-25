//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertWorkflowDispatchedTimes fails when workflow dispatch count for jobType does not match expected.

	// Example: assert workflow dispatch count
	f := queuefake.New()
	wf := f.Workflow()
	_, _ = wf.Chain(bus.NewJob("a", nil)).Dispatch(nil)
	_, _ = wf.Chain(bus.NewJob("a", nil)).Dispatch(nil)
	f.AssertWorkflowDispatchedTimes(nil, "a", 2)
}
