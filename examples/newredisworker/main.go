//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewRedisWorker creates a Redis-backed worker without exposing asynq handler types.

	// Example: new redis worker constructor
	_ = queue.NewRedisWorker
}
