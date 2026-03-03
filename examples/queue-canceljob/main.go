//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// CancelJob cancels a job via queue admin capability when supported.

	// Example: queue method cancel job
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	err := q.CancelJob(context.Background(), "job-id")
	_ = err
}
