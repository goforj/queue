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
	// ListJobs lists jobs via queue admin capability when supported.

	// Example: queue method list jobs
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	_, err = q.ListJobs(context.Background(), queue.ListJobsOptions{
		Queue: "default",
		State: queue.JobStatePending,
	})
	_ = err
}
