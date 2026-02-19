//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewWorkerpool creates an in-process workerpool queue runtime.

	// Example: new workerpool queue
	q, err := queue.NewWorkerpool()
	if err != nil {
		return
	}
	_ = q
}
