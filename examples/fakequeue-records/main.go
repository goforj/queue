//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// Records returns isolated records for accepted direct dispatches.
	// Chain and batch creation is available through ChainRecords and BatchRecords.

	// Example: read records
	fake := queue.NewFake()
	_ = fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
	records := fake.Records()
	fmt.Println(len(records), records[0].Job.Type)
	// Output: 1 emails:send
}
