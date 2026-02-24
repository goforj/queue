//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Observe records an event and updates normalized counters.

	// Example: observe event
	collector := queue.NewStatsCollector()
	collector.Observe(queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
}
