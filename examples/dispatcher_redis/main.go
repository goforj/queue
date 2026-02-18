//go:build ignore
// +build ignore

package main

import (
	"time"

	"github.com/goforj/queue"
)

func main() {
	dispatcher, err := queue.NewQueue(queue.QueueConfig{
		Driver:    queue.DriverRedis,
		RedisAddr: "127.0.0.1:6379",
	})
	if err != nil {
		return
	}

	_ = dispatcher.Dispatch(
		"emails:send",
		[]byte(`{"id":999}`),
		queue.WithQueue("critical"),
		queue.WithTimeout(30*time.Second),
		queue.WithMaxRetry(8),
		queue.WithDelay(2*time.Second),
		queue.WithUnique(1*time.Minute),
	)
	// Backoff is intentionally omitted; redis enqueue returns ErrBackoffUnsupported.
}
