//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewNull creates a drop-only queue runtime.

	// Example: new null queue
	q, err := queue.NewNull()
	if err != nil {
		return
	}
	_ = q.Dispatch(queue.NewJob("emails:send").Payload(map[string]int{"id": 1}).OnQueue("default"))
}
