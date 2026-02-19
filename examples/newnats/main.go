//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewNATS creates a NATS-backed queue runtime.

	// Example: new nats queue
	q, err := queue.NewNATS("nats://127.0.0.1:4222")
	if err != nil {
		return
	}
	_ = q
}
