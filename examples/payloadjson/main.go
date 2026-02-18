//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// PayloadJSON marshals payload as JSON.

	// Example: payload json
	task := queue.NewTask("emails:send").PayloadJSON(map[string]int{"id": 1})
	_ = task
}
