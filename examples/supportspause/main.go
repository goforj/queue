//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// SupportsPause reports whether a queue runtime supports Pause/Resume.

	// Example: check pause support
	q, _ := queue.NewSync()
	fmt.Println(queue.SupportsPause(q))
	// Output: true
}
