//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// Workflow returns the workflow/orchestration fake for chain/batch assertions.

	// Example: workflow fake
	f := queuefake.New()
	wf := f.Workflow()
	_, _ = wf.Chain(
		bus.NewJob("a", nil),
		bus.NewJob("b", nil),
	).Dispatch(context.Background())
	f.AssertChained(nil, []string{"a", "b"})
}
