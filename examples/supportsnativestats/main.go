//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.

	// Example: check native stats support
	q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
	fmt.Println(queue.SupportsNativeStats(q))
	// Output: true
}
