//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertBatched fails unless at least one recorded workflow batch matches predicate.

	// Example: assert batched jobs by predicate
	f := queuefake.New()
	_, _ = f.Workflow().Batch(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(nil)
	f.AssertBatched(nil, func(spec bus.BatchSpec) bool { return len(spec.Jobs) == 2 })
}
