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
	job := queue.NewJob("emails:send").Delay(300 * time.Millisecond)
	_ = task
}
