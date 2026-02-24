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
	example4()
}

func example1() {
	// Driver reports the configured backend driver for the underlying queue runtime.

	// Example: inspect queue driver
	var q queue.QueueRuntime
	driver := q.Driver()
	_ = driver
}

func example2() {
	// Example: fake driver
	fake := queue.NewFake()
	driver := fake.Driver()
	_ = driver
}

func example3() {
	// Example: local driver
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	fmt.Println(q.Driver())
	// Output: sync
}

func example4() {
	// Example: driver
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	fmt.Println(q.Driver())
	// Output: sync
}

