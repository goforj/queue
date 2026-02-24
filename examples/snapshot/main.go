//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Snapshot returns driver-native stats, falling back to collector data.

	// Example: snapshot from queue runtime
	q, _ := queue.NewSync()
	snapshot, _ := q.Stats(context.Background())
	_, ok := snapshot.Queue("default")
	fmt.Println(ok)
	// Output: true
}
