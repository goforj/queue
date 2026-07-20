//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// WithObserver installs one observer for queue, worker, and workflow lifecycle events.

	// Example: observe all queue activity
	observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		_ = event.Kind
	})
	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithObserver(observer))
	if err != nil {
		return
	}
	_ = q
}
