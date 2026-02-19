//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// StartWorkers is a no-op for fake queue.

	// Example: local start workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
}
