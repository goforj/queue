//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// TimelineHistoryFromSnapshot records queue counters and returns windowed points.
	// This is intended for drivers that don't expose native multi-point history.

	// Example: timeline history from snapshots
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Processed: 5, Failed: 1},
		},
	}
	points := queue.TimelineHistoryFromSnapshot(snapshot, "default", queue.QueueHistoryHour)
	fmt.Println(len(points) >= 1)
	// Output: true
}
