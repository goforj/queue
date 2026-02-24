package queue

import (
	"context"
	"testing"
)

func BenchmarkWorkerpoolLifecycle(b *testing.B) {
	ctx := context.Background()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q, err := newRuntime(Config{Driver: DriverWorkerpool})
		if err != nil {
			b.Fatalf("new workerpool queue failed: %v", err)
		}
		q.Register("bench:lifecycle", func(context.Context, Job) error { return nil })
		if err := q.Workers(1).StartWorkers(ctx); err != nil {
			b.Fatalf("start workerpool failed: %v", err)
		}
		if err := q.Shutdown(ctx); err != nil {
			b.Fatalf("shutdown workerpool failed: %v", err)
		}
	}
}
