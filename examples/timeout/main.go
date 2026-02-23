//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Timeout sets per-job execution timeout.

	// Example: timeout
	job := queue.NewJob("emails:send").Timeout(10 * time.Second)
	_ = job
}
