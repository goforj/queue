//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// RetryJob retries (runs now) a job via queue admin capability when supported.

	// Example: queue method retry job
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	err := q.RetryJob(context.Background(), "default", "job-id")
	_ = err
}
