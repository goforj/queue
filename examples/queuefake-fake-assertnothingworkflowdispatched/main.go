//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue/queuefake"

func main() {
	// AssertNothingWorkflowDispatched fails when any workflow dispatch was recorded.

	// Example: assert no workflow dispatches
	f := queuefake.New()
	f.AssertNothingWorkflowDispatched(nil)
}
