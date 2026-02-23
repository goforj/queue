//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewSQS creates a Queue on the SQS backend.

	// Example: sqs backend
	q, err := queue.NewSQS("us-east-1")
	if err != nil {
		return
	}
	_ = q
}
