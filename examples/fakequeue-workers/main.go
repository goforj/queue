//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Workers preserves fluent lifecycle compatibility without creating workers.

	// Example: set worker count
	fake := queue.NewFake()
	q := fake.Workers(4)
	fmt.Println(q != nil)
	// Output: true
}
