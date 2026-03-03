//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// SinglePointHistory converts a snapshot into a single current-history point.
	// This helper is intended for driver modules that do not expose historical buckets.

	// Example: single-point history
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Processed: 12, Failed: 1},
		},
	}
	points := queue.SinglePointHistory(snapshot, "default")
	fmt.Println(len(points), points[0].Processed, points[0].Failed)
	// Output: 1 12 1
}
