//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Normalize returns a safe options payload with defaults applied.

	// Example: normalize list options
	opts := queue.ListJobsOptions{Queue: "", State: "", Page: 0, PageSize: 1000}
	normalized := opts.Normalize()
	fmt.Println(normalized.Queue, normalized.State, normalized.Page, normalized.PageSize)
	// Output: default pending 1 500
}
