//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Snapshot returns a copy of collected counters.

	// Example: snapshot print
	collector := queue.NewStatsCollector()
	collector.Observe(queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
	collector.Observe(queue.Event{
		Kind:   queue.EventProcessStarted,
		Driver: queue.DriverSync,
		Queue:  "default",
		JobKey: "job-1",
		Time:   time.Now(),
	})
	collector.Observe(queue.Event{
		Kind:     queue.EventProcessSucceeded,
		Driver:   queue.DriverSync,
		Queue:    "default",
		JobKey:  "job-1",
		Duration: 12 * time.Millisecond,
		Time:     time.Now(),
	})
	snapshot := collector.Snapshot()
	counters, _ := snapshot.Queue("default")
	throughput, _ := snapshot.Throughput("default")
	fmt.Printf("queues=%v\n", snapshot.Queues())
	fmt.Printf("counters=%+v\n", counters)
	fmt.Printf("hour=%+v\n", throughput.Hour)
	// Output:
	// queues=[default]
	// counters={Pending:0 Active:0 Scheduled:0 Retry:0 Archived:0 Processed:1 Failed:0 Paused:0 AvgWait:0s AvgRun:12ms}
	// hour={Processed:1 Failed:0}
}
