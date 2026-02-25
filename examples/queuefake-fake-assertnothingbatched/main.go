//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue/queuefake"

func main() {
	// AssertNothingBatched fails if any workflow batch was recorded.

	// Example: assert no workflow batches
	f := queuefake.New()
	f.AssertNothingBatched(nil)
}
