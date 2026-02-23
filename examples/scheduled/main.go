//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Scheduled returns scheduled count for a queue.

	// Example: scheduled count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Scheduled: 4},
		},
	}
	fmt.Println(snapshot.Scheduled("default"))
	// Output: 4
}
