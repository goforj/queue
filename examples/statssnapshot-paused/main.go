//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Paused returns the observed pause state for a queue as zero or one.

	// Example: pause state getter
	collector := queue.NewStatsCollector()
	collector.Observe(context.Background(), queue.Event{
		Kind:   queue.EventQueuePaused,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
	snapshot := collector.Snapshot()
	fmt.Println(snapshot.Paused("default"))
	// Output: 1
}
