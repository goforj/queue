//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Pending returns pending count for a queue.

	// Example: pending count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Pending: 3},
		},
	}
	fmt.Println(snapshot.Pending("default"))
	// Output: 3
}
