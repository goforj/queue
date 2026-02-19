//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertDispatchedOn fails when taskType was not dispatched on queueName.

	// Example: assert task type dispatched on queue
	fake := queue.NewFake()
	_ = fake.Dispatch(
		queue.NewTask("emails:send").
			OnQueue("critical"),
	)
	fake.AssertDispatchedOn(nil, "critical", "emails:send")
}
