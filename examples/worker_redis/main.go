//go:build ignore
// +build ignore

package main

import (
	"context"

	"github.com/goforj/queue"
)

func main() {
	worker, err := queue.NewWorker(queue.WorkerConfig{
		Driver:    queue.DriverRedis,
		RedisAddr: "127.0.0.1:6379",
		Workers:   10,
	})
	if err != nil {
		return
	}

	worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
		return nil
	})

	_ = worker.Start()
	defer worker.Shutdown()
}
