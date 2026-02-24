//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Shutdown drains running work and releases resources.

	// Example: shutdown fake queue
	fake := queue.NewFake()
	err := fake.Shutdown(context.Background())
	_ = err
}
