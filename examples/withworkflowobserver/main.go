//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// WithWorkflowObserver installs a workflow lifecycle observer.

	// Example: workflow observer
	observer := queue.WorkflowObserverFunc(func(event queue.WorkflowEvent) {
		_ = event.Kind
	})
	q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithWorkflowObserver(observer))
	if err != nil {
		return
	}
	_ = q
}
