//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// Workers sets desired worker concurrency before StartWorkers.

	// Example: workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	q.Workers(4)
}
