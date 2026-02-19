//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// AssertCount fails when dispatch count is not expected.

	// Example: assert dispatch count
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewTask("emails:send"))
	fake.AssertCount(nil, 1)
}
