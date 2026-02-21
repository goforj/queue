//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// OnQueue sets the target queue name.

	// Example: on queue
	job := queue.NewJob("emails:send").OnQueue("critical")
	_ = task
}
