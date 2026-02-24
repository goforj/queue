//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// Driver returns the active queue driver.

	// Example: fake driver
	fake := queue.NewFake()
	driver := fake.Driver()
	_ = driver
}
