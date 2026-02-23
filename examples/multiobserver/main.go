//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// MultiObserver fans out events to multiple observers.

	// Example: fan out to multiple observers
	events := make(chan queue.Event, 2)
	observer := queue.MultiObserver(
		queue.ChannelObserver{Events: events},
		queue.ObserverFunc(func(queue.Event) {}),
	)
	observer.Observe(queue.Event{Kind: queue.EventEnqueueAccepted})
	fmt.Println(len(events))
	// Output: 1
}
