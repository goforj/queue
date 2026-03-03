//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// ListJobs lists jobs for a queue and state when supported.

	// Example: list jobs via helper
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	_, err := queue.ListJobs(context.Background(), q, queue.ListJobsOptions{
		Queue: "default",
		State: queue.JobStatePending,
	})
	_ = err
}
