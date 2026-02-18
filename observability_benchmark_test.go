package queue

import (
	"testing"
	"time"
)

func BenchmarkStatsCollectorObserve(b *testing.B) {
	collector := NewStatsCollector()
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		now := time.Now()
		taskKey := "bench-task"
		collector.Observe(Event{
			Kind:    EventEnqueueAccepted,
			Driver:  DriverSync,
			Queue:   "default",
			TaskKey: taskKey,
			Time:    now,
		})
		collector.Observe(Event{
			Kind:    EventProcessStarted,
			Driver:  DriverSync,
			Queue:   "default",
			TaskKey: taskKey,
			Time:    now.Add(1 * time.Millisecond),
		})
		collector.Observe(Event{
			Kind:     EventProcessSucceeded,
			Driver:   DriverSync,
			Queue:    "default",
			TaskKey:  taskKey,
			Duration: 2 * time.Millisecond,
			Time:     now.Add(3 * time.Millisecond),
		})
	}
}
