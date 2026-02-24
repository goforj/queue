//go:build integration

package root_test

import (
	"context"
	"testing"
	"time"

	"github.com/goforj/queue"
)

func benchmarkDispatchLoop(b *testing.B, ctx context.Context, q queue.QueueRuntime, job queue.Job) {
	if err := q.DispatchCtx(ctx, job); err != nil {
		b.Fatalf("warmup dispatch failed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := q.DispatchCtx(ctx, job); err != nil {
			b.Fatalf("dispatch failed: %v", err)
		}
	}
}

func benchJob(jobType, queueName string) queue.Job {
	return queue.NewJob(jobType).
		Payload(struct {
			ID   int    `json:"id"`
			Kind string `json:"kind"`
		}{
			ID:   1,
			Kind: queueName,
		}).
		OnQueue(queueName).
		Retry(2).
		Delay(0).
		Timeout(5 * time.Second)
}
