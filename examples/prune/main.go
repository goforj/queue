//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Prune deletes old workflow state records.

	// Example: prune workflow state
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	_ = q.Prune(context.Background(), time.Now().Add(-24*time.Hour))
}
