package queue

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkDriverDispatch_Local(b *testing.B) {
	ctx := context.Background()

	b.Run("null", func(b *testing.B) {
		q, err := NewNull()
		if err != nil {
			b.Fatalf("new null queue failed: %v", err)
		}
		benchmarkDispatchLoop(b, ctx, q, NewJob("bench:null").OnQueue("default"))
	})

	b.Run("sync", func(b *testing.B) {
		q, err := NewSync()
		if err != nil {
			b.Fatalf("new sync queue failed: %v", err)
		}
		q.Register("bench:sync", func(context.Context, Job) error { return nil })
		if err := q.Workers(1).StartWorkers(ctx); err != nil {
			b.Fatalf("start sync workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		benchmarkDispatchLoop(b, ctx, q, benchTask("bench:sync", "sync"))
	})

	b.Run("workerpool", func(b *testing.B) {
		q, err := NewWorkerpool()
		if err != nil {
			b.Fatalf("new workerpool queue failed: %v", err)
		}
		q.Register("bench:workerpool", func(context.Context, Job) error { return nil })
		if err := q.Workers(4).StartWorkers(ctx); err != nil {
			b.Fatalf("start workerpool failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		benchmarkDispatchLoop(b, ctx, q, benchTask("bench:workerpool", "workerpool"))
	})

	b.Run("database-sqlite", func(b *testing.B) {
		dsn := "file:" + filepath.Join(b.TempDir(), "queue-bench.db") + "?_busy_timeout=5000"
		q, err := New(Config{
			Driver:         DriverDatabase,
			DatabaseDriver: "sqlite",
			DatabaseDSN:    dsn,
			DefaultQueue:   "default",
		})
		if err != nil {
			b.Fatalf("new sqlite queue failed: %v", err)
		}
		q.Register("bench:sqlite", func(context.Context, Job) error { return nil })
		if err := q.StartWorkers(ctx); err != nil {
			b.Fatalf("start sqlite workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		benchmarkDispatchLoop(b, ctx, q, benchTask("bench:sqlite", "sqlite"))
	})
}

func benchmarkDispatchLoop(b *testing.B, ctx context.Context, q Queue, task Job) {
	// Warm up one dispatch so constructor/startup overhead stays out of the loop.
	if err := q.DispatchCtx(ctx, task); err != nil {
		b.Fatalf("warmup dispatch failed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := q.DispatchCtx(ctx, task); err != nil {
			b.Fatalf("dispatch failed: %v", err)
		}
	}
}

func benchTask(jobType, queueName string) Job {
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
