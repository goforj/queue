package queue

import (
	"context"
	"errors"
	"testing"
)

// TestFakeQueueDispatchNormalizesNilContext verifies internal runtime adapters may dispatch without pre-normalizing context.
func TestFakeQueueDispatchNormalizesNilContext(t *testing.T) {
	fake := NewFake()
	if err := fake.dispatch(nil, NewJob("emails:send")); err != nil {
		t.Fatalf("dispatch with nil context: %v", err)
	}
	fake.AssertDispatched(t, "emails:send")
}

// TestFakeQueueReadinessReflectsContext verifies the fake matches production readiness cancellation semantics.
func TestFakeQueueReadinessReflectsContext(t *testing.T) {
	fake := NewFake()
	if err := fake.Ready(nil); err != nil {
		t.Fatalf("Ready(nil): %v", err)
	}
	if err := fake.Ready(context.Background()); err != nil {
		t.Fatalf("Ready(background): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fake.Ready(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ready(canceled) = %v, want %v", err, context.Canceled)
	}
}

// TestFakeQueueNilHandleWithContextPreservesNil verifies derived handles do not turn an absent fake into a usable runtime.
func TestFakeQueueNilHandleWithContextPreservesNil(t *testing.T) {
	var fake *FakeQueue
	if got := fake.WithContext(context.Background()); got != nil {
		t.Fatalf("nil fake WithContext returned %T, want nil", got)
	}
}
