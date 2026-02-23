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
	example4()
}

func example1() {
	// Shutdown drains workers and closes underlying resources.

	// Example: shutdown runtime
	var q queue.QueueRuntime
	err := q.Shutdown(context.Background())
	_ = err
}

func example2() {
	// Example: local shutdown
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
	_ = q.Shutdown(context.Background())
}

func example3() {
	// Example: shutdown fake queue
	fake := queue.NewFake()
	err := fake.Shutdown(context.Background())
	_ = err
}

func example4() {
	// Example: shutdown
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
	_ = q.Shutdown(context.Background())
}

