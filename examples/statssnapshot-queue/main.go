//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Queue returns queue counters for a queue name.

	// Example: queue counters getter
	collector := queue.NewStatsCollector()
	collector.Observe(context.Background(), queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
	snapshot := collector.Snapshot()
	counters, ok := snapshot.Queue("default")
	fmt.Println(ok, counters.Pending)
	// Output: true 1
}
