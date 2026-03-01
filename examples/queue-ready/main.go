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
	// Ready validates queue backend readiness for dispatch/worker operation.

	// Example: queue ready
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	fmt.Println(q.Ready(context.Background()) == nil)
	// true
}
