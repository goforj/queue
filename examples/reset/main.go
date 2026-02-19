//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Reset clears all recorded dispatches.

	// Example: reset records
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewTask("emails:send").OnQueue("default"))
	fmt.Println(len(fake.Records()))
	fake.Reset()
	fmt.Println(len(fake.Records()))
	// Output:
	// 1
	// 0
}
