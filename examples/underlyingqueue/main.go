//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// UnderlyingQueue returns the low-level queue runtime used by this high-level runtime.

	// Example: underlying queue
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	raw := q.UnderlyingQueue()
	fmt.Println(raw.Driver())
	// Output: sync
}
