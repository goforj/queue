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
	// StartWorkers starts worker execution.

	// Example: start workers
	var q queue.QueueRuntime
	err := q.StartWorkers(context.Background())
	_ = err
}

func example2() {
	// Example: local start workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
}

func example3() {
	// Example: start fake workers
	fake := queue.NewFake()
	err := fake.StartWorkers(context.Background())
	_ = err
}

func example4() {
	// Example: start workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
}

