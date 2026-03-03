//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// SupportsQueueAdmin reports whether queue admin operations are available.

	// Example: detect admin support
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	fmt.Println(queue.SupportsQueueAdmin(q))
	// Output: true
}
