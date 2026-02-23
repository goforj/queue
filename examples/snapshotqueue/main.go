//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Queue.Stats returns driver-native stats.

	// Example: snapshot from queue
	q, _ := queue.NewSync()
	snapshot, _ := q.Stats(context.Background())
	_, ok := snapshot.Queue("default")
	fmt.Println(ok)
	// Output: true
}
