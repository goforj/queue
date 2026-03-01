//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Ready validates backend readiness for the provided queue runtime.

	// Example: ready via package helper
	q, _ := queue.NewSync()
	fmt.Println(queue.Ready(context.Background(), q) == nil)
	// true
}
