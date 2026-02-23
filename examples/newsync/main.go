//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewSync creates a Queue on the synchronous in-process backend.

	// Example: sync backend
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	_ = q
}
