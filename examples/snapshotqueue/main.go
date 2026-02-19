//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// SnapshotQueue returns driver-native stats, falling back to collector data.

	// Example: snapshot from queue runtime
	q, _ := queue.NewSync()
	snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
	_, ok := snapshot.Queue("default")
	fmt.Println(ok)
	// Output: true
}
