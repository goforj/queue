//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertDispatchedOn fails when jobType was not dispatched on queueName.

	// Example: assert job type dispatched on queue
	fake := queue.NewFake()
	_ = fake.Dispatch(
		queue.NewJob("emails:send").
			OnQueue("critical"),
	)
	fake.AssertDispatchedOn(nil, "critical", "emails:send")
}
