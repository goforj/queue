//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Stats returns a normalized snapshot when supported by the underlying driver.

	// Example: stats
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	if queue.SupportsNativeStats(q) {
		_, _ = q.Stats(context.Background())
	}
}
