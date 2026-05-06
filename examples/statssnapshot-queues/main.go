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
	// Queues returns sorted queue names present in the snapshot.

	// Example: list queues
	collector := queue.NewStatsCollector()
	collector.Observe(context.Background(), queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "critical",
		Time:   time.Now(),
	})
	snapshot := collector.Snapshot()
	names := snapshot.Queues()
	fmt.Println(len(names), names[0])
	// Output: 1 critical
}
