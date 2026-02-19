//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Retry returns retry count for a queue.

	// Example: retry
	task := queue.NewTask("emails:send").Retry(4)
	_ = task
	// Example: retry count getter
	snapshot := queue.StatsSnapshot{
		ByQueue: map[string]queue.QueueCounters{
			"default": {Retry: 1},
		},
	}
	fmt.Println(snapshot.Retry("default"))
	// Output: 1
}
