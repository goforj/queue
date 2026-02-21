//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertDispatchedTimes fails when taskType dispatch count does not match expected.

	// Example: assert task type dispatched times
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewJob("emails:send"))
	_ = fake.Dispatch(queue.NewJob("emails:send"))
	fake.AssertDispatchedTimes(nil, "emails:send", 2)
}
