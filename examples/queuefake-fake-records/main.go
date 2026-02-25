//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/queuefake"
)

func main() {
	// Records returns a copy of recorded dispatches.

	// Example: inspect recorded dispatches
	f := queuefake.New()
	_ = f.Queue().Dispatch(queue.NewJob("emails:send"))
	records := f.Records()
	_ = records
}
