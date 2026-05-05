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
	// Throughput returns rolling throughput windows for a queue name.

	// Example: throughput getter
	collector := queue.NewStatsCollector()
	collector.Observe(context.Background(), queue.Event{
		Kind:   queue.EventProcessSucceeded,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
	snapshot := collector.Snapshot()
	throughput, ok := snapshot.Throughput("default")
	fmt.Printf("ok=%v hour=%+v day=%+v week=%+v\n", ok, throughput.Hour, throughput.Day, throughput.Week)
	// Output: ok=true hour={Processed:1 Failed:0} day={Processed:1 Failed:0} week={Processed:1 Failed:0}
}
