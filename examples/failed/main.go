//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Failed returns failed count for a queue.

	// Example: failed count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Failed: 2},
		},
	}
	fmt.Println(snapshot.Failed("default"))
	// Output: 2
}
