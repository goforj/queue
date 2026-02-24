//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	example1()
	example2()
	example3()
}

func example1() {
	// Shutdown drains running work and releases resources.

	// Example: local shutdown
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
	_ = q.Shutdown(context.Background())
}

func example2() {
	// Example: shutdown fake queue
	fake := queue.NewFake()
	err := fake.Shutdown(context.Background())
	_ = err
}

func example3() {
	// Example: shutdown
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
	_ = q.Shutdown(context.Background())
}

