//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// WithWorkers sets desired worker concurrency before StartWorkers.

	// Example: workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	q.WithWorkers(4) // optional; default: runtime.NumCPU() (min 1)
}
