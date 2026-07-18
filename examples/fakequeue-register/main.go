//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Register is a compatibility no-op because the recording fake never executes handlers.

	// Example: register no-op on fake
	fake := queue.NewFake()
	fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
}
