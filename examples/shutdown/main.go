//go:build ignore
// +build ignore

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// Shutdown drains delayed and active local workerpool tasks.

	// Example: local shutdown
	dispatcher := queue.NewWorkerpoolDispatcher(queue.WorkerpoolConfig{Workers: 1, Buffer: 4})
	_ = dispatcher.Start(context.Background())
	_ = dispatcher.Shutdown(context.Background())
}
