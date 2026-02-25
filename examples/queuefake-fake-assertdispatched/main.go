//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertDispatched fails when jobType was not dispatched.

	// Example: assert queue dispatch by type
	f := queuefake.New()
	_ = f.Queue().Dispatch(queue.NewJob("emails:send"))
	f.AssertDispatched(nil, "emails:send")
}
