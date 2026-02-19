//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Workers sets desired worker concurrency before StartWorkers.

	// Example: set worker count
	fake := queue.NewFake()
	q := fake.Workers(4)
	fmt.Println(q != nil)
	// Output: true
}
