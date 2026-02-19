//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertNothingDispatched fails when any dispatch was recorded.

	// Example: assert nothing dispatched
	fake := queue.NewFake()
	fake.AssertNothingDispatched(nil)
}
