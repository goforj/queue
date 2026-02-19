//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// DispatchCtx submits a typed job payload using the provided context.

	// Example: dispatch with context
	fake := queue.NewFake()
	ctx := context.Background()
	err := fake.DispatchCtx(ctx, queue.NewTask("emails:send").OnQueue("default"))
	fmt.Println(err == nil)
	// Output: true
}
