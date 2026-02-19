//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// ResumeQueue resumes queue consumption for drivers that support it.

	// Example: resume queue
	q, _ := queue.NewSync()
	_ = queue.PauseQueue(context.Background(), q, "default")
	_ = queue.ResumeQueue(context.Background(), q, "default")
	snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 0
}
