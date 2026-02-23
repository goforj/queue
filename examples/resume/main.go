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
	if queue.SupportsPause(q.UnderlyingQueue()) {
		_ = q.Resume(context.Background(), "default")
	}
}

func example2() {
	// Example: resume queue
	q, _ := queue.NewSync()
	raw := q.UnderlyingQueue()
	_ = queue.Pause(context.Background(), raw, "default")
	_ = queue.Resume(context.Background(), raw, "default")
	snapshot, _ := queue.Snapshot(context.Background(), raw, nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 0
}

