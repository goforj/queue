//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Start initializes worker goroutines for workerpool mode.

	// Example: local start
	dispatcher := queue.NewWorkerpoolDispatcher(queue.WorkerpoolConfig{Workers: 1, Buffer: 4})
	_ = dispatcher.Start(context.Background())
}
