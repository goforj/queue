//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue/queuefake"

func main() {
	// AssertNotDispatched fails when jobType was dispatched.

	// Example: assert queue type was not dispatched
	f := queuefake.New()
	f.AssertNotDispatched(nil, "emails:send")
}
