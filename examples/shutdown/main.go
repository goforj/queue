//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Shutdown drains delayed and active local workerpool tasks.

	// Example: local shutdown
	q, err := queue.New(queue.Config{
		Driver: queue.DriverWorkerpool,
	})
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
	_ = q.Shutdown(context.Background())
}
