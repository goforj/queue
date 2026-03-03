//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// DeleteJob deletes a job via queue admin capability when supported.

	// Example: queue method delete job
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	err := q.DeleteJob(context.Background(), "default", "job-id")
	_ = err
}
