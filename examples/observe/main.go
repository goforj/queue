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
	// Observe handles a queue runtime event.

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
	// Example: observe event
	collector := queue.NewStatsCollector()
	collector.Observe(queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "default",
		Time:   time.Now(),
	})
}
