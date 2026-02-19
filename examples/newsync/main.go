//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewSync creates a synchronous in-process queue runtime.

	// Example: new sync queue
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	_ = q
}
