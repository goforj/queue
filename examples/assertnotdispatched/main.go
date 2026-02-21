//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertNotDispatched fails when jobType was dispatched.

	// Example: assert job type not dispatched
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewJob("emails:send"))
	fake.AssertNotDispatched(nil, "emails:cancel")
}
