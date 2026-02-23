//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewJob creates a job value with a required job type.

	// Example: new job
	job := queue.NewJob("emails:send")
	_ = job
}
