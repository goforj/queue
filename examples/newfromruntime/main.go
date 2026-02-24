//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewFromRuntime builds the high-level Queue API around an existing QueueRuntime.
	// 
	// This is an advanced constructor primarily intended for driver modules and custom
	// runtime integrations.

	// Example: wrap an existing runtime
	raw, err := queue.NewQueue(queue.Config{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	q, err := queue.NewFromRuntime(raw)
	if err != nil {
		return
	}
	_ = q
}
