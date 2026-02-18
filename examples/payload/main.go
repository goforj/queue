//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// Payload sets raw task payload bytes.

	// Example: payload bytes
	task := queue.NewTask("emails:send").Payload([]byte(`{"id":1}`))
	_ = task
}
