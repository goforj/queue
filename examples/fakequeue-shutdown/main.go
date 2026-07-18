//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Shutdown is a compatibility no-op because the recording fake owns no worker resources.

	// Example: shutdown fake queue
	fake := queue.NewFake()
	err := fake.Shutdown(context.Background())
	_ = err
}
