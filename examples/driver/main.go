//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Driver returns fake queue driver identity.

	// Example: local driver
	q, err := queue.NewSync()
	if err != nil {
		return
	}
	driverAware, ok := q.(interface{ Driver() queue.Driver })
	if !ok {
		return
	}
	fmt.Println(driverAware.Driver())
	// Output: sync
}
