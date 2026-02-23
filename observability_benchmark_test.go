package queue

import (
	"context"
	"testing"
	"time"
)

func BenchmarkStatsCollectorObserve(b *testing.B) {
	collector := NewStatsCollector()
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		now := time.Now()
		jobKey := "bench-job"
		collector.Observe(Event{
			Kind:   EventEnqueueAccepted,
			Driver: DriverSync,
			Queue:  "default",
			JobKey: jobKey,
			Time:   now,
		})
		collector.Observe(Event{
			Kind:   EventProcessStarted,
			Driver: DriverSync,
			Queue:  "default",
			JobKey: jobKey,
			Time:   now.Add(1 * time.Millisecond),
		})
		collector.Observe(Event{
			Kind:     EventProcessSucceeded,
			Driver:   DriverSync,
			Queue:    "default",
			JobKey:   jobKey,
			Duration: 2 * time.Millisecond,
			Time:     now.Add(3 * time.Millisecond),
		})
	}
}

func BenchmarkEnqueueSync_NoObserver(b *testing.B) {
	q, err := NewQueue(Config{Driver: DriverSync})
	if err != nil {
		b.Fatalf("new queue failed: %v", err)
	}
	q.Register("job:bench:no-observer", func(context.Context, Job) error { return nil })
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		b.Fatalf("start workers failed: %v", err)
	}
	defer q.Shutdown(context.Background())
	job := NewJob("job:bench:no-observer").OnQueue("default")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := q.DispatchCtx(context.Background(), job); err != nil {
			b.Fatalf("dispatch failed: %v", err)
		}
	}
}

func BenchmarkEnqueueSync_WithObserver(b *testing.B) {
	collector := NewStatsCollector()
	q, err := NewQueue(Config{
		Driver:   DriverSync,
		Observer: collector,
	})
	if err != nil {
		b.Fatalf("new queue failed: %v", err)
	}
	q.Register("job:bench:with-observer", func(context.Context, Job) error { return nil })
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		b.Fatalf("start workers failed: %v", err)
	}
	defer q.Shutdown(context.Background())
	job := NewJob("job:bench:with-observer").OnQueue("default")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := q.DispatchCtx(context.Background(), job); err != nil {
			b.Fatalf("dispatch failed: %v", err)
		}
	}
}
