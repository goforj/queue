//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/queue"
	"time"
)

func main() {
	// Backoff sets delay between retries.

	// Example: backoff
	job := queue.NewJob("emails:send").Backoff(500 * time.Millisecond)
	_ = job
}
