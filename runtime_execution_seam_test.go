package queue

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// TestOrdinaryExecutionWithoutCoordinator proves coordinator replacement cannot
// take ownership of ordinary delivery, registration, readiness, or lifecycle.
func TestOrdinaryExecutionWithoutCoordinator(t *testing.T) {
	for _, driver := range []Driver{DriverSync, DriverWorkerpool} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", driver, legacy), func(t *testing.T) {
				type contextKey string
				const decorated contextKey = "decorated"
				const wrapped contextKey = "wrapped"
				messages := make(chan Message, 1)
				opts := []Option{
					WithWorkers(1),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, decorated, true)
					}),
					WithMiddleware(MiddlewareFunc(func(ctx context.Context, message Message, next Next) error {
						if ctx.Value(decorated) != true {
							return fmt.Errorf("handler context decorator did not run before middleware")
						}
						return next(context.WithValue(ctx, wrapped, true), message)
					})),
				}
				if legacy {
					opts = append(opts, WithLegacyDirectEnvelope())
				}
				q, err := New(Config{Driver: driver}, opts...)
				if err != nil {
					t.Fatal(err)
				}
				// A missing coordinator makes any accidental delegation fail immediately.
				q.b = nil
				t.Cleanup(func() {
					if err := q.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
				})
				q.Register("seam:job", func(context.Context, Message) error {
					return fmt.Errorf("stale handler was invoked")
				})
				if err := q.StartWorkers(t.Context()); err != nil {
					t.Fatal(err)
				}
				q.Register("seam:job", func(ctx context.Context, message Message) error {
					if ctx.Value(wrapped) != true {
						return fmt.Errorf("middleware did not wrap the replacement handler")
					}
					messages <- message
					return nil
				})
				if err := q.Ready(t.Context()); err != nil {
					t.Fatal(err)
				}
				payload := []byte(`{"report":42}`)
				result, err := q.Dispatch(NewJob("seam:job").Payload(payload).Retry(0))
				if err != nil {
					t.Fatal(err)
				}
				select {
				case message := <-messages:
					if result.DispatchID == "" || message.DispatchID != result.DispatchID || message.JobID == "" || message.Attempt != 0 || message.JobType != "seam:job" || message.ChainID != "" || message.BatchID != "" {
						t.Fatalf("ordinary correlation changed: result=%+v message=%+v", result, message)
					}
					if !reflect.DeepEqual(message.PayloadBytes(), payload) {
						t.Fatalf("payload = %s, want %s", message.PayloadBytes(), payload)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("ordinary handler did not execute without a coordinator")
				}
				if err := q.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestRuntimeOptionProvenance distinguishes omitted settings from explicitly
// supplied zero values without changing established no-op and append semantics.
func TestRuntimeOptionProvenance(t *testing.T) {
	var omitted runtimeOptions
	omitted.apply([]Option{nil})
	if omitted.storeSupplied || omitted.clockSupplied || omitted.middlewareSupplied || omitted.workersSupplied || omitted.observerSupplied || omitted.decoratorSupplied || omitted.legacyEnvelopeSupplied {
		t.Fatal("omitted options acquired explicit provenance")
	}

	store := NewMemoryStore()
	clock := func() time.Time { return time.Unix(42, 0) }
	var order []int
	first := MiddlewareFunc(func(ctx context.Context, m Message, next Next) error {
		order = append(order, 1)
		return next(ctx, m)
	})
	second := MiddlewareFunc(func(ctx context.Context, m Message, next Next) error {
		order = append(order, 2)
		return next(ctx, m)
	})
	var observerCalls int
	observer := ObserverFunc(func(context.Context, Event) { observerCalls++ })
	var configured runtimeOptions
	configured.apply([]Option{
		WithStore(store), WithStore(nil), WithClock(clock), WithClock(nil),
		WithMiddleware(first, nil), WithMiddleware(second),
		WithWorkers(2), WithWorkers(0), WithWorkers(-1),
		WithObserver(observer), WithObserver(nil), WithObserver(observer),
		WithHandlerContextDecorator(func(ctx context.Context) context.Context { return ctx }),
		WithHandlerContextDecorator(nil), WithLegacyDirectEnvelope(),
	})
	if !configured.storeSupplied || !configured.clockSupplied || !configured.middlewareSupplied || !configured.workersSupplied || !configured.observerSupplied || !configured.decoratorSupplied || !configured.legacyEnvelopeSupplied {
		t.Fatal("explicit option provenance was lost")
	}
	if configured.store != store || !configured.clock().Equal(clock()) || configured.workers != 2 || configured.handlerContextDecorator != nil || !configured.legacyDirectEnvelope {
		t.Fatal("typed options changed overwrite or no-op behavior")
	}
	configured.observer.Observe(t.Context(), Event{})
	if observerCalls != 2 {
		t.Fatalf("composed observer calls = %d, want 2", observerCalls)
	}
	for _, middleware := range configured.middlewares {
		if middleware != nil {
			if err := middleware.Handle(t.Context(), Message{}, func(context.Context, Message) error { return nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("middleware order = %v", order)
	}

	var explicitZero runtimeOptions
	explicitZero.apply([]Option{WithStore(nil), WithClock(nil), WithMiddleware(), WithWorkers(0), WithObserver(nil), WithHandlerContextDecorator(nil)})
	if !explicitZero.storeSupplied || !explicitZero.clockSupplied || !explicitZero.middlewareSupplied || !explicitZero.workersSupplied || !explicitZero.observerSupplied || !explicitZero.decoratorSupplied {
		t.Fatal("explicit zero-value options became indistinguishable from omission")
	}
}
