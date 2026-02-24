//go:build integration
// +build integration

package bus_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/busruntime"
)

type failNthDispatchRuntime struct {
	inner   *bus.IntegrationTestRuntime
	n       int32
	target  int32
	failErr error
}

func newFailNthDispatchRuntime(target int, failErr error) *failNthDispatchRuntime {
	if target <= 0 {
		target = 1
	}
	if failErr == nil {
		failErr = errors.New("injected dispatch failure")
	}
	return &failNthDispatchRuntime{
		inner:   bus.NewIntegrationTestRuntime(),
		target:  int32(target),
		failErr: failErr,
	}
}

func (r *failNthDispatchRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	r.inner.BusRegister(jobType, handler)
}

func (r *failNthDispatchRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	if atomic.AddInt32(&r.n, 1) == r.target {
		return r.failErr
	}
	return r.inner.BusDispatch(ctx, jobType, payload, opts)
}

func (r *failNthDispatchRuntime) StartWorkers(ctx context.Context) error { return r.inner.StartWorkers(ctx) }
func (r *failNthDispatchRuntime) Shutdown(ctx context.Context) error     { return r.inner.Shutdown(ctx) }

func TestSQLStore_RuntimeChainInitialDispatchFailureStateConsistent(t *testing.T) {
	q := newFailNthDispatchRuntime(1, errors.New("chain enqueue failed"))
	store := newSQLiteStoreForRuntime(t)

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var catchCount atomic.Int32
	var finallyCount atomic.Int32

	chainID, err := b.Chain(bus.NewJob("monitor:poll", nil)).
		Catch(func(context.Context, bus.ChainState, error) error {
			catchCount.Add(1)
			return nil
		}).
		Finally(func(context.Context, bus.ChainState) error {
			finallyCount.Add(1)
			return nil
		}).
		Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected chain dispatch error")
	}
	if chainID == "" {
		t.Fatal("expected chain ID on dispatch failure")
	}
	if got := catchCount.Load(); got != 1 {
		t.Fatalf("expected catch once, got %d", got)
	}
	if got := finallyCount.Load(); got != 1 {
		t.Fatalf("expected finally once, got %d", got)
	}

	st, err := b.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !st.Failed {
		t.Fatalf("expected failed chain state, got %+v", st)
	}
	if st.Failure == "" {
		t.Fatalf("expected chain failure message, got %+v", st)
	}
}

func TestSQLStore_RuntimeBatchPartialDispatchFailureStateConsistent(t *testing.T) {
	// Fail the second internal dispatch so the first batch job can be enqueued and
	// processed, then the batch builder returns an enqueue error on subsequent work.
	q := newFailNthDispatchRuntime(2, errors.New("batch enqueue failed after first job"))
	store := newSQLiteStoreForRuntime(t)

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })

	var catchCount atomic.Int32
	var finallyCount atomic.Int32
	batchID, err := b.Batch(
		bus.NewJob("monitor:poll", nil),
		bus.NewJob("monitor:poll", nil),
	).Catch(func(context.Context, bus.BatchState, error) error {
		catchCount.Add(1)
		return nil
	}).Finally(func(context.Context, bus.BatchState) error {
		finallyCount.Add(1)
		return nil
	}).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected batch dispatch error")
	}
	if batchID == "" {
		t.Fatal("expected batch ID on partial dispatch failure")
	}

	st, err := b.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	// Current contract for partial enqueue failure after progress has begun:
	// return the batch ID + error without force-cancelling or firing callbacks.
	if st.Completed || st.Cancelled {
		t.Fatalf("expected incomplete batch after partial dispatch failure, got %+v", st)
	}
	if st.Processed != 1 || st.Pending != 1 || st.Failed != 0 {
		t.Fatalf("expected processed=1 pending=1 failed=0, got %+v", st)
	}
	if got := catchCount.Load(); got != 0 {
		t.Fatalf("expected catch not invoked on partial dispatch failure-after-progress, got %d", got)
	}
	if got := finallyCount.Load(); got != 0 {
		t.Fatalf("expected finally not invoked on partial dispatch failure-after-progress, got %d", got)
	}
}
