//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Processed returns processed count for a queue.

	// Example: processed count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Processed: 11},
		},
	}
	fmt.Println(snapshot.Processed("default"))
	// Output: 11
}
