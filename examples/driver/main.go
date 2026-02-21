//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	example1()
	example2()
	example3()
}

func example1() {
	// Driver returns the active queue driver.

	// Example: inspect queue driver
	var q queue.Queue
	driver := q.Driver()
	_ = driver
}

func example2() {
	// Example: fake driver
	fake := queue.NewFake()
	driver := fake.Driver()
	_ = driver
}

func example3() {
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

