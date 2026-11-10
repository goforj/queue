//go:build integration

package temporal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/sdk/client"
)

// BenchmarkDriverDispatch_Integration measures accepted starts through the public driver, excluding worker startup and activity completion.
func BenchmarkDriverDispatch_Integration(b *testing.B) {
	b.Run("temporal", func(b *testing.B) {
		requireTemporalServer(b)
		name := fmt.Sprintf("bench-temporal-%d", time.Now().UnixNano())
		q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
			DriverBaseConfig:       queueconfig.DriverBaseConfig{DefaultQueue: name},
			ClientOptions:          client.Options{HostPort: temporalAddress, ConnectionOptions: client.ConnectionOptions{GetSystemInfoTimeout: 20 * time.Second}},
			DefaultActivityTimeout: 5 * time.Second,
			ShutdownTimeout:        5 * time.Second,
		}, queue.WithWorkers(2))
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := q.Shutdown(ctx); err != nil {
				b.Error(err)
			}
		})
		q.Register("bench:temporal", func(context.Context, queue.Message) error { return nil })
		if err := q.StartWorkers(b.Context()); err != nil {
			b.Fatal(err)
		}
		job := queue.NewJob("bench:temporal").Payload(struct {
			ID   int    `json:"id"`
			Kind string `json:"kind"`
		}{ID: 1, Kind: name}).OnQueue(name).Retry(2).Delay(0).Timeout(5 * time.Second)
		if _, err := q.WithContext(b.Context()).Dispatch(job); err != nil {
			b.Fatalf("warmup dispatch failed: %v", err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := q.WithContext(b.Context()).Dispatch(job); err != nil {
				b.Fatalf("dispatch failed: %v", err)
			}
		}
		b.StopTimer()
	})
}
