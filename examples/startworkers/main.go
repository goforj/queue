//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// StartWorkers starts worker execution.

	// Example: local start workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
}
