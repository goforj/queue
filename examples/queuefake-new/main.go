//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// New creates a fake queue harness backed by queue.NewFake().

	// Example: queuefake harness
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
	f.AssertDispatched(t, "emails:send")
	f.AssertCount(t, 1)
}
