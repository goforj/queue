//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// PayloadJSON marshals payload as JSON.

	// Example: payload json
	job := queue.NewJob("emails:send").PayloadJSON(map[string]int{"id": 1})
	_ = job
}
