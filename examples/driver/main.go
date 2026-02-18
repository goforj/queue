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
	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
	if err != nil {
		return
	}
	fmt.Println(dispatcher.Driver())
	// Output: sync
}
