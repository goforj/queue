//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/redisqueue"
)

func main() {
	// RetryJob retries (runs now) a job when supported.

	// Example: retry a job via helper
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	err = queue.RetryJob(context.Background(), q, "default", "job-id")
	_ = err
}
