package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue"
)

// Fake preserves the legacy bus testing surface as a thin view of the
// concurrency-safe root queue fake.
//
// Deprecated: use queue.NewFake.
type Fake struct {
	queue *queue.FakeQueue
}

var _ Bus = (*Fake)(nil)

var fakeInitializationMu sync.Mutex

// NewFake creates a legacy workflow view over one canonical root fake.
// @group Constructors
//
// Example: new bus fake
//
//	fake := bus.NewFake()
//	_, _ = fake.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil))
func NewFake() *Fake {
	return &Fake{queue: queue.NewFake()}
}

// Queue returns the canonical root fake shared by this compatibility view.
// @group Testing
func (f *Fake) Queue() *queue.FakeQueue {
	return f.canonicalQueue()
}

// canonicalQueue lazily initializes the historical zero value under a package
// lock while keeping Fake values safe to copy after construction.
func (f *Fake) canonicalQueue() *queue.FakeQueue {
	fakeInitializationMu.Lock()
	defer fakeInitializationMu.Unlock()
	if f.queue == nil {
		f.queue = queue.NewFake()
	}
	return f.queue
}

// Register is inert because Fake records accepted intent instead of executing handlers.
func (f *Fake) Register(string, Handler) {}

// Dispatch converts and records a legacy job through the canonical fake.
// @group Testing
//
// Example: record dispatch
//
//	fake := bus.NewFake()
//	_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil))
func (f *Fake) Dispatch(ctx context.Context, job Job) (DispatchResult, error) {
	converted, err := toQueueJob(job)
	if err != nil {
		return DispatchResult{}, err
	}
	if err := f.canonicalQueue().WithContext(ctx).Dispatch(converted); err != nil {
		return DispatchResult{}, err
	}
	return DispatchResult{DispatchID: "fake"}, nil
}

// Chain snapshots legacy jobs and delegates execution-time conversion to the
// same compatibility builder used by a production queue facade.
// @group Testing
func (f *Fake) Chain(jobs ...Job) ChainBuilder {
	return &queueChainBuilder{
		queue: f.canonicalQueue(),
		jobs:  append([]Job(nil), jobs...),
	}
}

// Batch snapshots legacy jobs and delegates execution-time conversion to the
// same compatibility builder used by a production queue facade.
// @group Testing
func (f *Fake) Batch(jobs ...Job) BatchBuilder {
	return &queueBatchBuilder{
		queue: f.canonicalQueue(),
		jobs:  append([]Job(nil), jobs...),
	}
}

// StartWorkers delegates the inert lifecycle contract to the canonical fake.
func (f *Fake) StartWorkers(ctx context.Context) error {
	if f == nil {
		return nil
	}
	return f.canonicalQueue().StartWorkers(ctx)
}

// Shutdown delegates the inert lifecycle contract to the canonical fake.
func (f *Fake) Shutdown(ctx context.Context) error {
	if f == nil {
		return nil
	}
	return f.canonicalQueue().Shutdown(ctx)
}

// FindBatch returns state created by an accepted fake batch.
func (f *Fake) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	if f == nil {
		return BatchState{}, ErrNotFound
	}
	return f.canonicalQueue().FindBatch(ctx, batchID)
}

// FindChain returns state created by an accepted fake chain.
func (f *Fake) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	if f == nil {
		return ChainState{}, ErrNotFound
	}
	return f.canonicalQueue().FindChain(ctx, chainID)
}

// Prune applies workflow retention to the canonical fake store.
func (f *Fake) Prune(ctx context.Context, before time.Time) error {
	if f == nil {
		return nil
	}
	return f.canonicalQueue().Prune(ctx, before)
}

// AssertNothingDispatched fails if any direct job was accepted.
// @group Testing
func (f *Fake) AssertNothingDispatched(t testing.TB) {
	t.Helper()
	f.canonicalQueue().AssertNothingDispatched(t)
}

// AssertDispatched fails if the given job type was never accepted.
// @group Testing
func (f *Fake) AssertDispatched(t testing.TB, jobType string) {
	t.Helper()
	f.canonicalQueue().AssertDispatched(t, jobType)
}

// AssertDispatchedTimes fails if the accepted count for jobType does not match n.
// @group Testing
func (f *Fake) AssertDispatchedTimes(t testing.TB, jobType string, n int) {
	t.Helper()
	f.canonicalQueue().AssertDispatchedTimes(t, jobType, n)
}

// AssertNotDispatched fails if the given job type was accepted.
// @group Testing
func (f *Fake) AssertNotDispatched(t testing.TB, jobType string) {
	t.Helper()
	f.canonicalQueue().AssertNotDispatched(t, jobType)
}

// AssertCount fails if the total direct dispatch count does not match n.
// @group Testing
func (f *Fake) AssertCount(t testing.TB, n int) {
	t.Helper()
	f.canonicalQueue().AssertCount(t, n)
}

// AssertDispatchedOn fails if a job type was not accepted on queueName.
// @group Testing
func (f *Fake) AssertDispatchedOn(t testing.TB, queueName, jobType string) {
	t.Helper()
	f.canonicalQueue().AssertDispatchedOn(t, queueName, jobType)
}

// AssertChained fails if no accepted chain matches the expected job order.
// @group Testing
func (f *Fake) AssertChained(t testing.TB, expected []string) {
	t.Helper()
	f.canonicalQueue().AssertChained(t, expected)
}

// AssertBatchCount fails if the accepted batch count does not match n.
// @group Testing
func (f *Fake) AssertBatchCount(t testing.TB, n int) {
	t.Helper()
	f.canonicalQueue().AssertBatchCount(t, n)
}

// AssertNothingBatched fails if any batch was accepted.
// @group Testing
func (f *Fake) AssertNothingBatched(t testing.TB) {
	t.Helper()
	f.canonicalQueue().AssertNothingBatched(t)
}

// AssertBatched fails unless one canonical batch matches the legacy projection.
// The predicate runs outside recorder locks.
// @group Testing
func (f *Fake) AssertBatched(t testing.TB, predicate func(spec BatchSpec) bool) {
	t.Helper()
	for _, record := range f.canonicalQueue().BatchRecords() {
		spec := BatchSpec{JobTypes: make([]string, 0, len(record.Jobs))}
		for _, job := range record.Jobs {
			spec.JobTypes = append(spec.JobTypes, job.Job.Type)
		}
		if predicate(spec) {
			return
		}
	}
	t.Fatalf("expected at least one batch to match predicate")
}

// BatchSpec is the frozen assertion projection retained for legacy source compatibility.
type BatchSpec struct {
	JobTypes []string
}
