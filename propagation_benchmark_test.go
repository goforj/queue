package queue

import (
	"context"
	"testing"
)

// benchmarkPropagationKey distinguishes publisher metadata from extracted identity at the logical handler boundary.
type benchmarkPropagationKey string

// benchmarkContextPropagator models a generic W3C-shaped carrier without adding a tracing SDK dependency.
type benchmarkContextPropagator struct{}

// Inject returns fresh metadata so queue serialization includes the application propagator's normal snapshot cost.
func (benchmarkContextPropagator) Inject(ctx context.Context) map[string]string {
	identity, _ := ctx.Value(benchmarkPropagationKey("publisher")).(string)
	if identity == "" {
		return nil
	}
	return map[string]string{"traceparent": identity, "tracestate": "vendor=benchmark"}
}

// Extract binds transported identity separately so a shared in-process publisher context cannot satisfy the delivery check.
func (benchmarkContextPropagator) Extract(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, benchmarkPropagationKey("receiver"), headers["traceparent"])
}

// BenchmarkContextPropagationDispatch exercises the public high-level queue, including its serialized logical-job envelope.
func BenchmarkContextPropagationDispatch(b *testing.B) {
	const identity = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const payload = `{"order_id":"order-42","account_id":"account-7","total":12900}`
	for _, mode := range []string{"disabled", "configured_empty", "valid_carrier"} {
		b.Run(mode, func(b *testing.B) {
			var options []Option
			if mode != "disabled" {
				options = append(options, WithContextPropagator(benchmarkContextPropagator{}))
			}
			queue, err := NewSync(options...)
			if err != nil {
				b.Fatal(err)
			}
			if err := queue.StartWorkers(context.Background()); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := queue.Shutdown(context.Background()); err != nil {
					b.Error(err)
				}
			})
			calls := 0
			var received Message
			var receivedIdentity string
			queue.Register("orders.process", func(ctx context.Context, message Message) error {
				calls++
				received = message
				receivedIdentity, _ = ctx.Value(benchmarkPropagationKey("receiver")).(string)
				return nil
			})
			ctx := context.Background()
			wantIdentity := ""
			if mode == "valid_carrier" {
				ctx = context.WithValue(ctx, benchmarkPropagationKey("publisher"), identity)
				wantIdentity = identity
			}
			handle := queue.WithContext(ctx)
			job := NewJob("orders.process").Payload([]byte(payload)).OnQueue("orders")
			if _, err := handle.Dispatch(job); err != nil {
				b.Fatal(err)
			}
			if calls != 1 || received.JobType != "orders.process" || string(received.PayloadBytes()) != payload || receivedIdentity != wantIdentity {
				b.Fatalf("warm delivery calls=%d type=%q payload=%s carrier=%q", calls, received.JobType, received.PayloadBytes(), receivedIdentity)
			}
			calls = 0
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := handle.Dispatch(job); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if calls != b.N || received.JobType != "orders.process" || string(received.PayloadBytes()) != payload || receivedIdentity != wantIdentity {
				b.Fatalf("measured delivery calls=%d type=%q payload=%s carrier=%q", calls, received.JobType, received.PayloadBytes(), receivedIdentity)
			}
			b.ReportMetric(1, "deliveries/op")
		})
	}
}
