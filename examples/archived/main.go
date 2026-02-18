//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Archived returns archived count for a queue.

	// Example: archived count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Archived: 7},
		},
	}
	fmt.Println(snapshot.Archived("default"))
	// Output: 7
}
