//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewNATS creates a Queue on the NATS backend.

	// Example: nats backend
	q, err := queue.NewNATS("nats://127.0.0.1:4222")
	if err != nil {
		return
	}
	_ = q
}
