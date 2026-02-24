//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Driver reports the configured backend driver for the underlying queue runtime.

	// Example: driver
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	fmt.Println(q.Driver())
	// Output: sync
}
