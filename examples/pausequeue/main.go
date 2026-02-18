//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// PauseQueue pauses queue consumption for drivers that support it.

	// Example: pause queue
	q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
	_ = queue.PauseQueue(context.Background(), q, "default")
	snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 1
}
