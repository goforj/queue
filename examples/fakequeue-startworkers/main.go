//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// StartWorkers is a compatibility no-op because the recording fake owns no workers.

	// Example: start fake workers
	fake := queue.NewFake()
	err := fake.StartWorkers(context.Background())
	_ = err
}
