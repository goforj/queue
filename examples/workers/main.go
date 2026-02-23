//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	example1()
	example2()
	example3()
}

func example1() {
	// Workers sets desired worker concurrency before StartWorkers.

	// Example: set worker count
	var q queue.QueueRuntime
	q = q.Workers(4)
}

func example2() {
	// Example: set worker count
	fake := queue.NewFake()
	q := fake.Workers(4)
	fmt.Println(q != nil)
	// Output: true
}

func example3() {
	// Example: workers
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	q.Workers(4)
}

