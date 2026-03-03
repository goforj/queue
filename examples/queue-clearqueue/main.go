//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
)

func main() {
	// ClearQueue clears queue jobs via queue admin capability when supported.

	// Example: queue method clear queue
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	err := q.ClearQueue(context.Background(), "default")
	_ = err
}
