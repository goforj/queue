//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// NewRedisDispatcher creates a redis-backed dispatcher using an asynq-compatible enqueuer.

	// Example: new redis dispatcher
	dispatcher := queue.NewRedisDispatcher(nil)
	fmt.Println(dispatcher.Driver())
}
