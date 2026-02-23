//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Queue.Pause pauses queue consumption for drivers that support it.

	// Example: pause queue
	q, _ := queue.NewSync()
	_ = q.Pause(context.Background(), "default")
	snapshot, _ := q.Stats(context.Background())
	fmt.Println(snapshot.Paused("default"))
	// Output: 1
}
