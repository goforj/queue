//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue/queuefake"

func main() {
	// AssertWorkflowNotDispatched fails when jobType was workflow-dispatched.

	// Example: assert workflow not dispatched
	f := queuefake.New()
	f.AssertWorkflowNotDispatched(nil, "emails:send")
}
