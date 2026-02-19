//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Records returns a copy of all dispatch records.

	// Example: read records
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewTask("emails:send").OnQueue("default"))
	records := fake.Records()
	fmt.Println(len(records), records[0].Task.Type)
	// Output: 1 emails:send
}
