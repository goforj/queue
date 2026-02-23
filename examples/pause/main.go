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
	// Pause pauses consumption for a queue when supported by the underlying driver.

	// Example: pause queue
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	if queue.SupportsPause(q.UnderlyingQueue()) {
		_ = q.Pause(context.Background(), "default")
	}
}

func example2() {
	// Example: pause queue
	q, _ := queue.NewSync()
	_ = queue.Pause(context.Background(), q.UnderlyingQueue(), "default")
	snapshot, _ := queue.Snapshot(context.Background(), q.UnderlyingQueue(), nil)
	fmt.Println(snapshot.Paused("default"))
	// Output: 1
}

