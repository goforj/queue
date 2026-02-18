//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Driver returns the local dispatcher's driver mode.

	// Example: local driver
	q, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
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
