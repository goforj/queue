//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// WithHandlerContextDecorator decorates queue handler execution context before
	// process lifecycle events and handler execution run.

	// Example: decorate handler context
	q, err := queue.New(
		queue.Config{Driver: queue.DriverSync},
		queue.WithHandlerContextDecorator(func(ctx context.Context) context.Context {
			return context.WithValue(ctx, "source", "jobs")
		}),
	)
	if err != nil {
		return
	}
	_ = q
}
