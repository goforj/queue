//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertChained fails if no recorded workflow chain matches expected job type order.

	// Example: assert chain sequence
	f := queuefake.New()
	_, _ = f.Workflow().Chain(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(nil)
	f.AssertChained(nil, []string{"a", "b"})
}
