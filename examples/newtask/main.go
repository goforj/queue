//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewTask creates a task value with a required task type.

	// Example: new task
	task := queue.NewTask("emails:send")
	_ = task
}
