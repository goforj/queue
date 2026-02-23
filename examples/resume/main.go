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
	// Resume resumes queue consumption for drivers that support it.

	// Example: resume queue
	q, _ := queue.NewSync()
	raw := q.UnderlyingQueue()
	_ = queue.Pause(context.Background(), raw, "default")
	_ = queue.Resume(context.Background(), raw, "default")
	snapshot, _ := queue.Snapshot(context.Background(), raw, nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 0
}
