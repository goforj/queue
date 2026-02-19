//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Shutdown is a no-op for fake queue.

	// Example: local shutdown
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
	_ = q.Shutdown(context.Background())
}
