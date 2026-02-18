//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Paused returns paused count for a queue.

	// Example: paused count getter
	collector := queue.NewStatsCollector()
	collector.Observe(queue.Event{
		Kind:   queue.EventQueuePaused,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
	snapshot := collector.Snapshot()
	fmt.Println(snapshot.Paused("default"))
	// Output: 1
}
