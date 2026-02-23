//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// WithStore overrides the workflow orchestration store.

	// Example: workflow store
	var store queue.WorkflowStore
	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithStore(store))
	if err != nil {
		return
	}
	_ = q
}
