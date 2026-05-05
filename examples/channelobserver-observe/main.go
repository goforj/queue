//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"

	"github.com/goforj/queue"
)

func main() {
	// Observe forwards an event to the configured channel.

	// Example: channel observer
	ch := make(chan queue.Event, 1)
	observer := queue.ChannelObserver{Events: ch}
	observer.Observe(context.Background(), queue.Event{Kind: queue.EventProcessStarted, Queue: "default"})
	event := <-ch
	_ = event
}
