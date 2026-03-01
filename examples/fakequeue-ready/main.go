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
	// Ready validates fake queue readiness.

	// Example: fake ready
	fake := queue.NewFake()
	fmt.Println(fake.Ready(context.Background()) == nil)
	// true
}
