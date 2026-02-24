//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// DispatchCtx enqueues a high-level job using the provided context.

	// Example: dispatch with context
	fake := queue.NewFake()
	ctx := context.Background()
	err := fake.DispatchCtx(ctx, queue.NewJob("emails:send").OnQueue("default"))
	fmt.Println(err == nil)
	// Output: true
}
