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
	// ClearQueue clears queue jobs when supported.

	// Example: clear queue via helper
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	err = queue.ClearQueue(context.Background(), q, "default")
	_ = err
}
