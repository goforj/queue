package temporalproof

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// proofStop is a candidate arbiter, not a production lifecycle implementation.
// Its completion means SDK Stop returned, not that user callbacks have drained.
type proofStop struct {
	once sync.Once
	done chan struct{}
	stop func()
}

// newProofStop retains one stop operation beyond any individual caller deadline.
func newProofStop(stop func()) *proofStop {
	return &proofStop{done: make(chan struct{}), stop: stop}
}

// wait starts or joins one operation without latching caller-specific timeouts.
func (s *proofStop) wait(ctx context.Context) error {
	s.once.Do(func() { go func() { s.stop(); close(s.done) }() })
	select {
	case <-s.done:
		return nil
	default:
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestStopArbiterCallerTimeoutCanRejoin proves cancellation does not authorize
// early cleanup or prevent later and concurrent callers joining the same stop.
func TestStopArbiterCallerTimeoutCanRejoin(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	stop := newProofStop(func() { calls.Add(1); close(entered); <-release })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := stop.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait=%v", err)
	}
	<-entered
	select {
	case <-stop.done:
		t.Fatal("stop reported completion while still blocked")
	default:
	}
	results := make(chan error, 32)
	for i := 0; i < cap(results); i++ {
		go func() { results <- stop.wait(t.Context()) }()
	}
	close(release)
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("Stop called %d times", calls.Load())
	}
	if err := stop.wait(ctx); err != nil {
		t.Fatalf("completed stop latched caller cancellation: %v", err)
	}
}
