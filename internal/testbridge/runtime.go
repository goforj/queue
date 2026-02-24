package testbridge

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/runtimehook"
)

// RuntimeFromQueue extracts the internal low-level runtime from a high-level
// queue.Queue for integration test wiring. It delegates to an internal hook
// registered by package queue to avoid reflect/unsafe access here.
func RuntimeFromQueue(q *queue.Queue) (any, error) {
	if runtimehook.ExtractRuntimeFromQueue == nil {
		return nil, fmt.Errorf("queue internal runtime extract hook is not registered")
	}
	return runtimehook.ExtractRuntimeFromQueue(q)
}
