//go:build ignore
// +build ignore

package main

import (
	"github.com/goforj/queue"
	"time"
)

func main() {
	// UniqueFor enables uniqueness dedupe within the given TTL.

	// Example: unique for
	task := queue.NewTask("emails:send").UniqueFor(45 * time.Second)
	_ = task
}
