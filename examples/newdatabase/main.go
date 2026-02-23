//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewDatabase creates a Queue on the SQL backend.

	// Example: database backend
	q, err := queue.NewDatabase("sqlite", "file:queue.db?_busy_timeout=5000")
	if err != nil {
		return
	}
	_ = q
}
