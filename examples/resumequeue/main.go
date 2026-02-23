//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Queue.Resume resumes queue consumption for drivers that support it.

	// Example: resume queue
	q, _ := queue.NewSync()
	_ = q.Pause(context.Background(), "default")
	_ = q.Resume(context.Background(), "default")
	snapshot, _ := q.Stats(context.Background())
	fmt.Println(snapshot.Paused("default"))
	// Output: 0
}
