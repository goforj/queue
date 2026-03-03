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
	// History returns queue history points via queue admin capability when supported.

	// Example: queue method history
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	points, err = q.History(context.Background(), "default", queue.QueueHistoryHour)
	_ = points
	_ = err
}
