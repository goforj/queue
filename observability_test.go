package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func startTestQueue(t *testing.T, q queueRuntime) {
	t.Helper()
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
}

func TestStatsCollector_CapturesQueueProcessing(t *testing.T) {
	collector := NewStatsCollector()
	q, err := newRuntime(Config{
		Driver:   DriverSync,
		Observer: collector,
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	startTestQueue(t, q)
	q.Register("job:obs:ok", func(_ context.Context, _ Job) error { return nil })
	if err := q.DispatchCtx(context.Background(), NewJob("job:obs:ok").Payload([]byte(`{}`)).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	stats := collector.Snapshot()
	counters, ok := stats.ByQueue["default"]
	if !ok {
		t.Fatal("expected default queue counters")
	}
	if counters.Processed < 1 {
		t.Fatalf("expected processed >= 1, got %d", counters.Processed)
	}
	if counters.Active != 0 {
		t.Fatalf("expected active = 0, got %d", counters.Active)
	}
}

func TestStatsCollector_CapturesProcessingFailure(t *testing.T) {
	collector := NewStatsCollector()
	q, err := newRuntime(Config{
		Driver:   DriverSync,
		Observer: collector,
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	startTestQueue(t, q)
	q.Register("job:obs:fail", func(_ context.Context, _ Job) error { return errors.New("boom") })
	_ = q.DispatchCtx(context.Background(), NewJob("job:obs:fail").Payload([]byte(`{}`)).OnQueue("default").Retry(0))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := collector.Snapshot()
		counters := stats.ByQueue["default"]
		if counters.Failed >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected failed counter to be incremented")
}

func TestStatsSnapshot_Getters(t *testing.T) {
	collector := NewStatsCollector()
	now := time.Now()
	collector.Observe(Event{
		Kind:   EventEnqueueAccepted,
		Driver: DriverSync,
		Queue:  "default",
		Time:   now,
	})
	collector.Observe(Event{
		Kind:     EventProcessStarted,
		Driver:   DriverSync,
		Queue:    "default",
		JobKey:   "job-1",
		Time:     now.Add(10 * time.Millisecond),
		Duration: 5 * time.Millisecond,
	})
	collector.Observe(Event{
		Kind:     EventProcessSucceeded,
		Driver:   DriverSync,
		Queue:    "default",
		JobKey:   "job-1",
		Time:     now.Add(20 * time.Millisecond),
		Duration: 5 * time.Millisecond,
	})

	snapshot := collector.Snapshot()
	counters, ok := snapshot.Queue("default")
	if !ok {
		t.Fatal("expected default queue from getter")
	}
	if counters.Processed < 1 {
		t.Fatalf("expected processed >= 1, got %d", counters.Processed)
	}
	throughput, ok := snapshot.Throughput("default")
	if !ok {
		t.Fatal("expected default throughput from getter")
	}
	if throughput.Hour.Processed < 1 {
		t.Fatalf("expected hour processed >= 1, got %d", throughput.Hour.Processed)
	}
	names := snapshot.Queues()
	if len(names) != 1 || names[0] != "default" {
		t.Fatalf("expected queue names [default], got %v", names)
	}
}

func TestObserverPanic_DoesNotBreakDispatch(t *testing.T) {
	q, err := newRuntime(Config{
		Driver: DriverSync,
		Observer: ObserverFunc(func(Event) {
			panic("observer panic")
		}),
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	startTestQueue(t, q)
	q.Register("job:panic:enqueue", func(_ context.Context, _ Job) error { return nil })
	if err := q.DispatchCtx(context.Background(), NewJob("job:panic:enqueue").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
}

func TestObserverPanic_DoesNotBreakHandlerExecution(t *testing.T) {
	var called atomic.Int64
	q, err := newRuntime(Config{
		Driver: DriverSync,
		Observer: ObserverFunc(func(Event) {
			panic("observer panic")
		}),
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	startTestQueue(t, q)
	q.Register("job:panic:handler", func(_ context.Context, _ Job) error {
		called.Add(1)
		return nil
	})
	if err := q.DispatchCtx(context.Background(), NewJob("job:panic:handler").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if called.Load() != 1 {
		t.Fatalf("expected handler to run once, got %d", called.Load())
	}
}

func TestMultiObserverPanic_DoesNotBlockOtherObservers(t *testing.T) {
	var received atomic.Int64
	observer := MultiObserver(
		ObserverFunc(func(Event) {
			panic("observer panic")
		}),
		ObserverFunc(func(Event) {
			received.Add(1)
		}),
	)
	observer.Observe(Event{Kind: EventEnqueueAccepted, Queue: "default"})
	if received.Load() != 1 {
		t.Fatalf("expected second observer to receive event, got %d", received.Load())
	}
}
