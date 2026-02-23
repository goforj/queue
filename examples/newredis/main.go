//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewRedis creates a Queue on the Redis backend.

	// Example: redis backend
	q, err := queue.NewRedis("127.0.0.1:6379")
	if err != nil {
		return
	}
	_ = q
}
