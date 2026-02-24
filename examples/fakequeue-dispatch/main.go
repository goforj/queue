//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// Dispatch records a typed job payload in-memory using the fake default queue.

	// Example: dispatch to fake queue
	fake := queue.NewFake()
	err := fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
	_ = err
}
