//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// RetryCount returns retry count for a queue.

	// Example: retry count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Retry: 1},
		},
	}
	fmt.Println(snapshot.RetryCount("default"))
	// Output: 1
}
