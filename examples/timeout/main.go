//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Timeout sets per-task execution timeout.

	// Example: timeout
	task := queue.NewTask("emails:send").Timeout(10 * time.Second)
	_ = task
}
