//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Start initializes worker goroutines for workerpool mode.

	// Example: local start
	q, err := queue.New(queue.Config{
		Driver: queue.DriverWorkerpool,
	})
	if err != nil {
		return
	}
	_ = q.Start(context.Background())
}
