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
	// BatchRecords returns isolated creation records for accepted fake batches.

	// Example: inspect a fake batch
	fake := queue.NewFake()
	_, _ = fake.Batch(
		queue.NewJob("emails:first"),
		queue.NewJob("emails:second"),
	).Name("nightly").AllowFailures().Dispatch(context.Background())
	record := fake.BatchRecords()[0]
	fmt.Println(record.Name, len(record.Jobs), record.AllowFailed)
	// Output: nightly 2 true
}
