//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Delay defers execution by duration.

	// Example: delay
	task := queue.NewTask("emails:send").Delay(300 * time.Millisecond)
	_ = task
}
