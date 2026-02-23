//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	example1()
	example2()
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

