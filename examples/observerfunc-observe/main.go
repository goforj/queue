//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"log/slog"
	"os"
)

func main() {
	// Observe calls the wrapped function.

	// Example: observer func logging hook
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	observer := queue.ObserverFunc(func(event queue.Event) {
		logger.Info("queue event",
			"kind", event.Kind,
			"driver", event.Driver,
			"queue", event.Queue,
			"job_type", event.JobType,
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
		JobType: "emails:send",
	})
}
