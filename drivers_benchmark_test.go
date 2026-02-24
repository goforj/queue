package queue

import (
	"context"
	"testing"
	"time"
)

func BenchmarkDriverDispatch_Local(b *testing.B) {
	ctx := context.Background()

	b.Run("null", func(b *testing.B) {
		q, err := NewQueue(Config{Driver: DriverNull})
		if err != nil {
			b.Fatalf("new null queue failed: %v", err)
		}
		benchmarkDispatchLoop(b, ctx, q, NewJob("bench:null").OnQueue("default"))
	})

	b.Run("sync", func(b *testing.B) {
		q, err := NewQueue(Config{Driver: DriverSync})
		if err != nil {
			b.Fatalf("new sync queue failed: %v", err)
		}
		q.Register("bench:sync", func(context.Context, Job) error { return nil })
		if err := q.Workers(1).StartWorkers(ctx); err != nil {
			b.Fatalf("start sync workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		benchmarkDispatchLoop(b, ctx, q, benchJob("bench:sync", "sync"))
	})

	b.Run("workerpool", func(b *testing.B) {
		q, err := NewQueue(Config{Driver: DriverWorkerpool})
		if err != nil {
			b.Fatalf("new workerpool queue failed: %v", err)
		}
		q.Register("bench:workerpool", func(context.Context, Job) error { return nil })
		if err := q.Workers(4).StartWorkers(ctx); err != nil {
			b.Fatalf("start workerpool failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		benchmarkDispatchLoop(b, ctx, q, benchJob("bench:workerpool", "workerpool"))
	})

}

func benchmarkDispatchLoop(b *testing.B, ctx context.Context, q QueueRuntime, job Job) {
	// Warm up one dispatch so constructor/startup overhead stays out of the loop.
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

func benchJob(jobType, queueName string) Job {
	return NewJob(jobType).
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
