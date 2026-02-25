//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue/queuefake"

func main() {
	// AssertNothingDispatched fails when any dispatch was recorded.

	// Example: assert no queue dispatches
	f := queuefake.New()
	f.AssertNothingDispatched(nil)
}
