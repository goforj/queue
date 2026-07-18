//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// ChainRecords returns isolated creation records for accepted fake chains.

	// Example: inspect a fake chain
	fake := queue.NewFake()
	_, _ = fake.Chain(
		queue.NewJob("reports:build"),
		queue.NewJob("reports:publish"),
	).OnQueue("workflow").Dispatch(context.Background())
	record := fake.ChainRecords()[0]
	fmt.Println(len(record.Nodes), record.Queue)
	// Output: 2 workflow
}
