//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewRedis creates a Redis-backed queue runtime.

	// Example: new redis queue
	q, err := queue.NewRedis("127.0.0.1:6379")
	if err != nil {
		return
	}
	_ = q
}
