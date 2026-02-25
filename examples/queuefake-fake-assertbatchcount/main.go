//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertBatchCount fails if total recorded workflow batch count does not match n.

	// Example: assert workflow batch count
	f := queuefake.New()
	_, _ = f.Workflow().Batch(bus.NewJob("a", nil)).Dispatch(nil)
	f.AssertBatchCount(nil, 1)
}
