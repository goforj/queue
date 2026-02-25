//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// AssertCount fails when total dispatch count is not expected.

	// Example: assert total queue dispatch count
	f := queuefake.New()
	q := f.Queue()
	_ = q.Dispatch(queue.NewJob("a"))
	_ = q.Dispatch(queue.NewJob("b"))
	f.AssertCount(nil, 2)
}
