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
	_ = queue.Pause(context.Background(), q, "default")
	_ = queue.Resume(context.Background(), q, "default")
	snapshot, _ := queue.Snapshot(context.Background(), q, nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 0
}
