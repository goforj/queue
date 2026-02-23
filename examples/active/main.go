//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Active returns active count for a queue.

	// Example: active count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Active: 2},
		},
	}
	fmt.Println(snapshot.Active("default"))
	// Output: 2
}
