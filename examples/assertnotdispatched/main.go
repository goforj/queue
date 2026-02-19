//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertNotDispatched fails when taskType was dispatched.

	// Example: assert task type not dispatched
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewTask("emails:send"))
	fake.AssertNotDispatched(nil, "emails:cancel")
}
