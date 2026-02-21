//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/queue"
	"log/slog"
	"os"
	"time"
)

func main() {
	example1()
	example2()
	example3()
	example4()
}

func example1() {
	// Observe handles a queue runtime event.

	// Example: observe runtime event
	var observer queue.Observer
	observer.Observe(queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "default",
	})
}

func example2() {
	// Example: observer func logging hook
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	observer := queue.ObserverFunc(func(event queue.Event) {
		logger.Info("queue event",
			"kind", event.Kind,
			"driver", event.Driver,
			"queue", event.Queue,
			"task_type", event.TaskType,
			"attempt", event.Attempt,
			"max_retry", event.MaxRetry,
			"duration", event.Duration,
			"err", event.Err,
		)
	})
	observer.Observe(queue.Event{
		Kind:     queue.EventProcessSucceeded,
		Driver:   queue.DriverSync,
		Queue:    "default",
		TaskType: "emails:send",
	})
}

func example3() {
	// Example: channel observer
	ch := make(chan queue.Event, 1)
	observer := queue.ChannelObserver{Events: ch}
	observer.Observe(queue.Event{Kind: queue.EventProcessStarted, Queue: "default"})
	event := <-ch
	_ = event
}

func example4() {
	// Example: observe event
	collector := queue.NewStatsCollector()
	collector.Observe(queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
}

