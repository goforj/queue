package queue

import (
	"context"
	"testing"
)

// propagationKey separates transport metadata from receiver-owned values in these contract tests.
type propagationKey string

// testContextPropagator supplies deterministic carrier values without introducing a tracing SDK dependency.
type testContextPropagator struct{ headers map[string]string }

// Inject returns the caller-controlled metadata used to check snapshot and input limits.
func (p testContextPropagator) Inject(context.Context) map[string]string { return p.headers }

// Extract restores only the selected correlation key onto the receiver context.
func (testContextPropagator) Extract(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, propagationKey("trace"), headers["traceparent"])
}

// TestContextPropagationOptionReachesLogicalHandlers covers the actual queue execution seam through its existing delivery envelope.
func TestContextPropagationOptionReachesLogicalHandlers(t *testing.T) {
	options := []Option{WithContextPropagator(testContextPropagator{headers: map[string]string{"traceparent": "producer"}})}
	q, err := NewSync(options...)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	q.Register("orders.process", func(ctx context.Context, message Message) error {
		calls++
		if ctx.Value(propagationKey("trace")) != "producer" || string(message.PayloadBytes()) != `{"value":"exact"}` {
			t.Error("handler lost propagated context or exact payload")
		}
		return nil
	})
	if _, err := q.Dispatch(NewJob("orders.process").Payload([]byte(`{"value":"exact"}`))); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("handler calls=%d", calls)
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
