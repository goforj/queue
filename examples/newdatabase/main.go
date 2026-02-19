//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewDatabase creates a SQL-backed queue runtime.

	// Example: new database queue
	q, err := queue.NewDatabase("sqlite", "file:queue.db?_busy_timeout=5000")
	if err != nil {
		return
	}
	_ = q
}
