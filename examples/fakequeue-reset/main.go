//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Reset clears direct dispatches and all workflow records through every fake view.

	// Example: reset records
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
	fmt.Println(len(fake.Records()))
	fake.Reset()
	fmt.Println(len(fake.Records()))
	// Output:
	// 1
	// 0
}
