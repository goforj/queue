//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// WithWorkers sets desired worker concurrency before StartWorkers.
	// It applies to high-level queue constructors (for example NewWorkerpool/New/NewSync).

	// Example: constructor workers option
	q, err := queue.NewWorkerpool(
		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
	)
	if err != nil {
		return
	}
	_ = q
}
