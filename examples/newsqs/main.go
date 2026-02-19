//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewSQS creates an SQS-backed queue runtime.

	// Example: new sqs queue
	q, err := queue.NewSQS("us-east-1")
	if err != nil {
		return
	}
	_ = q
}
