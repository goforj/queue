package temporalqueue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAdmissionRetainsActualWork verifies timeout never authorizes dependency cleanup.
func TestAdmissionRetainsActualWork(t *testing.T) {
	gate := newAdmission()
	release, err := gate.enter()
	if err != nil {
		t.Fatal(err)
	}
	gate.close()
	if _, err := gate.enter(); !errors.Is(err, errAdmissionClosed) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if err := gate.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	release()
	release()
	if err := gate.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestStopOperationRejoin verifies concurrent callers preserve a single non-idempotent SDK stop.
func TestStopOperationRejoin(t *testing.T) {
	var calls atomic.Int32
	unblock := make(chan struct{})
	stop := newStopOperation(func() { calls.Add(1); <-unblock })
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if err := stop.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := stop.wait(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	close(unblock)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("stop calls = %d", calls.Load())
	}
}
