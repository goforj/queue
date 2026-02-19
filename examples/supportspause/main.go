//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// SupportsPause reports whether a queue runtime supports PauseQueue/ResumeQueue.

	// Example: check pause support
	q, _ := queue.NewSync()
	fmt.Println(queue.SupportsPause(q))
	// Output: true
}
