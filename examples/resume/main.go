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
	example1()
	example2()
}

func example1() {
	// Resume resumes consumption for a queue when supported by the underlying driver.

	// Example: resume queue
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	if queue.SupportsPause(q) {
		_ = q.Resume(context.Background(), "default")
	}
}

func example2() {
	// Example: resume queue
	q, _ := queue.NewSync()
	_ = queue.Pause(context.Background(), q, "default")
	_ = queue.Resume(context.Background(), q, "default")
	snapshot, _ := queue.Snapshot(context.Background(), q, nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 0
}

