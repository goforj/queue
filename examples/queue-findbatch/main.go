//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// FindBatch returns current batch state by ID.

	// Example: find batch
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	q.Register("emails:send", func(ctx context.Context, j queue.Context) error { return nil })
	batchID, err := q.Batch(queue.NewJob("emails:send")).Dispatch(context.Background())
	if err != nil {
		return
	}
	_, _ = q.FindBatch(context.Background(), batchID)
}
