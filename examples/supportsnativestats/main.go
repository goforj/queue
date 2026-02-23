//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.

	// Example: check native stats support
	q, _ := queue.NewSync()
	fmt.Println(queue.SupportsNativeStats(q.UnderlyingQueue()))
	// Output: true
}
