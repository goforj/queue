//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertDispatched fails when jobType was not dispatched.

	// Example: assert job type dispatched
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewJob("emails:send"))
	fake.AssertDispatched(nil, "emails:send")
}
