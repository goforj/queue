//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"

	"github.com/goforj/queue"
)

func main() {
	// WithObserver installs a workflow lifecycle observer.

	// Example: workflow observer
	observer := queue.WorkflowObserverFunc(func(ctx context.Context, event queue.WorkflowEvent) {
		_ = event.Kind
	})
	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithObserver(observer))
	if err != nil {
		return
	}
	_ = q
}
