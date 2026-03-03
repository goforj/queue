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
	// QueueHistory returns queue history points when supported.

	// Example: history via helper
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	_, err = queue.QueueHistory(context.Background(), q, "default", queue.QueueHistoryHour)
	_ = err
}
