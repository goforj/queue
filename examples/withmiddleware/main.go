//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// WithMiddleware appends queue workflow middleware.

	// Example: middleware
	mw := queue.MiddlewareFunc(func(ctx context.Context, j queue.Context, next queue.Next) error {
		return next(ctx, j)
	})
	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithMiddleware(mw))
	if err != nil {
		return
	}
	_ = q
}
