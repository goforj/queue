package observation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSinkSupportsLateConcurrentObservers verifies construction-time injection can be extended safely before and during event delivery.
func TestSinkSupportsLateConcurrentObservers(t *testing.T) {
	sink := NewSink[int]()
	if sink.HasObservers() {
		t.Fatal("new sink unexpectedly has observers")
	}
	sink.Add(nil)
	if sink.HasObservers() {
		t.Fatal("nil callback unexpectedly enabled the sink")
	}

	var calls atomic.Int64
	sink.Add(func(context.Context, int) { panic("isolated") })
	sink.Add(func(ctx context.Context, value int) {
		if ctx == nil {
			t.Error("observer received a nil context")
		}
		calls.Add(int64(value))
	})
	if !sink.HasObservers() {
		t.Fatal("sink did not report registered observers")
	}
	sink.Observe(nil, 2)
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 after panic-isolated delivery", calls.Load())
	}

	var workers sync.WaitGroup
	for range 8 {
		workers.Add(2)
		go func() {
			defer workers.Done()
			sink.Add(func(context.Context, int) {})
		}()
		go func() {
			defer workers.Done()
			sink.Observe(context.Background(), 1)
		}()
	}
	workers.Wait()
}
