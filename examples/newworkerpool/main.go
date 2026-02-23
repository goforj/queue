//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewWorkerpool creates a Queue on the in-process workerpool backend.

	// Example: workerpool backend
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q
}
