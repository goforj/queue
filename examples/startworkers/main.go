//go:build ignore
// +build ignore

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
	// StartWorkers starts worker execution.

	// Example: start workers
	var q queue.Queue
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

