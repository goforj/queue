//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Pause pauses consumption for a queue when supported by the underlying driver.

	// Example: pause queue
	q, _ := queue.NewSync()
	_ = queue.Pause(context.Background(), q.UnderlyingQueue(), "default")
	snapshot, _ := queue.Snapshot(context.Background(), q.UnderlyingQueue(), nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 1
}
