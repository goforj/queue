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
	example1()
	example2()
}

func example1() {
	// DispatchCtx submits a typed job payload using the provided context.

	// Example: dispatch with context
	var q queue.QueueRuntime
	err := q.DispatchCtx(
		context.Background(),
		queue.NewJob("emails:send").OnQueue("default"),
	)
	_ = err
}

func example2() {
	// Example: dispatch with context
	fake := queue.NewFake()
	ctx := context.Background()
	err := fake.DispatchCtx(ctx, queue.NewJob("emails:send").OnQueue("default"))
	fmt.Println(err == nil)
	// Output: true
}

