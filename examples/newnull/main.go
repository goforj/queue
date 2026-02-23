//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewNull creates a Queue on the null backend.

	// Example: null backend
	q, err := queue.NewNull()
	if err != nil {
		return
	}
	_ = q
}
