package bus

import (
	"context"
	"testing"
	"time"
)

type Fake struct {
	dispatched []Job
	chains     [][]Job
	batches    [][]Job
}

var _ Bus = (*Fake)(nil)

// NewFake creates a bus fake that records dispatch, chain, and batch calls.
// @group Constructors
//
// Example: new bus fake
//
//	fake := bus.NewFake()
//	_, _ = fake.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil))
func NewFake() *Fake {
	return &Fake{}
}

func (f *Fake) Register(string, Handler) {}

func (f *Fake) Dispatch(_ context.Context, job Job) (DispatchResult, error) {
	f.dispatched = append(f.dispatched, job)
	return DispatchResult{DispatchID: "fake"}, nil
}

func (f *Fake) Chain(jobs ...Job) ChainBuilder {
	f.chains = append(f.chains, append([]Job(nil), jobs...))
	return &fakeChain{fake: f}
}

func (f *Fake) Batch(jobs ...Job) BatchBuilder {
	f.batches = append(f.batches, append([]Job(nil), jobs...))
	return &fakeBatch{fake: f}
}

func (f *Fake) StartWorkers(context.Context) error { return nil }
func (f *Fake) Shutdown(context.Context) error     { return nil }
func (f *Fake) FindBatch(context.Context, string) (BatchState, error) {
	return BatchState{}, ErrNotFound
}
func (f *Fake) FindChain(context.Context, string) (ChainState, error) {
	return ChainState{}, ErrNotFound
}
func (f *Fake) Prune(context.Context, time.Time) error { return nil }

func (f *Fake) AssertNothingDispatched(t testing.TB) {
	t.Helper()
	if len(f.dispatched) != 0 {
		t.Fatalf("expected no dispatched jobs, got %d", len(f.dispatched))
	}
}

func (f *Fake) AssertDispatched(t testing.TB, jobType string) {
	t.Helper()
	for _, j := range f.dispatched {
		if j.Type == jobType {
			return
		}
	}
	t.Fatalf("expected dispatched job %q", jobType)
}

func (f *Fake) AssertDispatchedTimes(t testing.TB, jobType string, n int) {
	t.Helper()
	var got int
	for _, j := range f.dispatched {
		if j.Type == jobType {
			got++
		}
	}
	if got != n {
		t.Fatalf("expected job %q dispatched %d times, got %d", jobType, n, got)
	}
}

func (f *Fake) AssertNotDispatched(t testing.TB, jobType string) {
	t.Helper()
	for _, j := range f.dispatched {
		if j.Type == jobType {
			t.Fatalf("expected job %q not dispatched", jobType)
		}
	}
}

func (f *Fake) AssertCount(t testing.TB, n int) {
	t.Helper()
	if len(f.dispatched) != n {
		t.Fatalf("expected dispatched count %d, got %d", n, len(f.dispatched))
	}
}

func (f *Fake) AssertDispatchedOn(t testing.TB, queueName, jobType string) {
	t.Helper()
	for _, j := range f.dispatched {
		if j.Type == jobType && j.Options.Queue == queueName {
			return
		}
	}
	t.Fatalf("expected job %q dispatched on queue %q", jobType, queueName)
}

func (f *Fake) AssertChained(t testing.TB, expected []string) {
	t.Helper()
	for _, chain := range f.chains {
		if len(chain) != len(expected) {
			continue
		}
		ok := true
		for i := range chain {
			if chain[i].Type != expected[i] {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("expected chain %v", expected)
}

func (f *Fake) AssertBatchCount(t testing.TB, n int) {
	t.Helper()
	if len(f.batches) != n {
		t.Fatalf("expected batch count %d, got %d", n, len(f.batches))
	}
}

func (f *Fake) AssertNothingBatched(t testing.TB) {
	t.Helper()
	if len(f.batches) != 0 {
		t.Fatalf("expected no batches, got %d", len(f.batches))
	}
}

func (f *Fake) AssertBatched(t testing.TB, predicate func(spec BatchSpec) bool) {
	t.Helper()
	for _, b := range f.batches {
		spec := BatchSpec{JobTypes: make([]string, 0, len(b))}
		for _, job := range b {
			spec.JobTypes = append(spec.JobTypes, job.Type)
		}
		if predicate(spec) {
			return
		}
	}
	t.Fatalf("expected at least one batch to match predicate")
}

type BatchSpec struct {
	JobTypes []string
}

type fakeChain struct{ fake *Fake }

func (f *fakeChain) OnQueue(string) ChainBuilder { return f }
func (f *fakeChain) Catch(func(context.Context, ChainState, error) error) ChainBuilder {
	return f
}
func (f *fakeChain) Finally(func(context.Context, ChainState) error) ChainBuilder { return f }
func (f *fakeChain) Dispatch(context.Context) (string, error)                     { return "fake-chain", nil }

type fakeBatch struct{ fake *Fake }

func (f *fakeBatch) Name(string) BatchBuilder                                      { return f }
func (f *fakeBatch) OnQueue(string) BatchBuilder                                   { return f }
func (f *fakeBatch) AllowFailures() BatchBuilder                                   { return f }
func (f *fakeBatch) Progress(func(context.Context, BatchState) error) BatchBuilder { return f }
func (f *fakeBatch) Then(func(context.Context, BatchState) error) BatchBuilder     { return f }
func (f *fakeBatch) Catch(func(context.Context, BatchState, error) error) BatchBuilder {
	return f
}
func (f *fakeBatch) Finally(func(context.Context, BatchState) error) BatchBuilder { return f }
func (f *fakeBatch) Dispatch(context.Context) (string, error)                     { return "fake-batch", nil }
