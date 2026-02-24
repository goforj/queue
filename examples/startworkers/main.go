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
	// StartWorkers starts worker processing.

	// Example: local start workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
}

func example2() {
	// Example: start fake workers
	fake := queue.NewFake()
	err := fake.StartWorkers(context.Background())
	_ = err
}

func example3() {
	// Example: start workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q.StartWorkers(context.Background())
}

