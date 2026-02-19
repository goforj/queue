//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// NewFake creates a queue fake that records dispatches and provides assertions.

	// Example: fake queue assertions
	fake := queue.NewFake()
	_ = fake.Dispatch(
		queue.NewTask("emails:send").
			Payload(map[string]any{"id": 1}).
			OnQueue("critical"),
	)
	records := fake.Records()
	fmt.Println(len(records), records[0].Queue, records[0].Task.Type)
	// Output: 1 critical emails:send
}
