//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewJob creates a task value with a required task type.

	// Example: new task
	job := queue.NewJob("emails:send")
	_ = task
}
