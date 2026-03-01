package queue

import (
	"context"
	"fmt"
)

// Ready validates backend readiness for the provided queue runtime.
// @group Observability
//
// Example: ready via package helper
//
//	q, _ := queue.NewSync()
//	fmt.Println(queue.Ready(context.Background(), q) == nil)
//	// true
func Ready(ctx context.Context, q any) error {
	raw := resolveQueueRuntime(q)
	if raw == nil {
		return fmt.Errorf("runtime is nil")
	}
	return runtimeReadyCheck(ctx, raw)
}
