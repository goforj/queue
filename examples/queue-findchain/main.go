//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// FindChain returns current chain state by ID.

	// Example: find chain
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	q.Register("first", func(ctx context.Context, m queue.Message) error { return nil })
	chainID, err := q.Chain(queue.NewJob("first")).Dispatch(context.Background())
	if err != nil {
		return
	}
	_, _ = q.FindChain(context.Background(), chainID)
}
