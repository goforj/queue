//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// Retry sets max retry attempts.

	// Example: retry
	job := queue.NewJob("emails:send").Retry(4)
	_ = task
}
