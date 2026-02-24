//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// Observe handles a queue runtime event.

	// Example: observe runtime event
	var observer queue.Observer
	observer.Observe(queue.Event{
		Kind:   queue.EventEnqueueAccepted,
		Driver: queue.DriverSync,
		Queue:  "default",
	})
}
