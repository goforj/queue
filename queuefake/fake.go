package queuefake

import (
	"testing"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

// Fake exposes a queue test harness with assertion helpers for dispatched jobs.
// It wraps queue.NewFake() so tests can inject a queue fake without external services.
// @group Testing
type Fake struct {
	q *queue.FakeQueue
	b *bus.Fake
}

// New creates a fake queue harness backed by queue.NewFake().
// @group Testing
//
// Example: queuefake harness
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
//	f.AssertDispatched(t, "emails:send")
//	f.AssertCount(t, 1)
func New() *Fake {
	return &Fake{
		q: queue.NewFake(),
		b: bus.NewFake(),
	}
}

// Queue returns the queue fake to inject into code under test.
// @group Testing
//
// Example: queue fake
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
func (f *Fake) Queue() *queue.FakeQueue { return f.q }

// Workflow returns the workflow/orchestration fake for chain/batch assertions.
// @group Testing
//
// Example: workflow fake
//
//	f := queuefake.New()
//	wf := f.Workflow()
//	_, _ = wf.Chain(
//		bus.NewJob("a", nil),
//		bus.NewJob("b", nil),
//	).Dispatch(context.Background())
//	f.AssertChained(t, []string{"a", "b"})
func (f *Fake) Workflow() *bus.Fake { return f.b }

// Reset clears recorded dispatches.
// @group Testing
//
// Example: reset recorded dispatches
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("emails:send"))
//	f.Reset()
//	f.AssertNothingDispatched(t)
func (f *Fake) Reset() { f.q.Reset() }

// Records returns a copy of recorded dispatches.
// @group Testing
//
// Example: inspect recorded dispatches
//
//	f := queuefake.New()
//	_ = f.Queue().Dispatch(queue.NewJob("emails:send"))
//	records := f.Records()
//	_ = records
func (f *Fake) Records() []queue.DispatchRecord { return f.q.Records() }

// Count returns the total number of recorded dispatches.
// @group Testing
//
// Example: count total dispatches
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("a"))
//	_ = q.Dispatch(queue.NewJob("b"))
//	_ = f.Count()
func (f *Fake) Count() int { return len(f.q.Records()) }

// CountJob returns how many times a job type was dispatched.
// @group Testing
//
// Example: count by job type
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("emails:send"))
//	_ = q.Dispatch(queue.NewJob("emails:send"))
//	_ = f.CountJob("emails:send")
func (f *Fake) CountJob(jobType string) int {
	var count int
	for _, rec := range f.q.Records() {
		if rec.Job.Type == jobType {
			count++
		}
	}
	return count
}

// CountOn returns how many times a job type was dispatched on a queue.
// @group Testing
//
// Example: count by queue and job type
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("critical"))
//	_ = f.CountOn("critical", "emails:send")
func (f *Fake) CountOn(queueName, jobType string) int {
	var count int
	for _, rec := range f.q.Records() {
		if rec.Queue == queueName && rec.Job.Type == jobType {
			count++
		}
	}
	return count
}

// Workflow assertion wrappers (forwarded to bus.Fake) keep tests queuefake-first.

// AssertNothingWorkflowDispatched fails when any workflow dispatch was recorded.
// @group Testing
//
// Example: assert no workflow dispatches
//
//	f := queuefake.New()
//	f.AssertNothingWorkflowDispatched(t)
func (f *Fake) AssertNothingWorkflowDispatched(t testing.TB) { f.b.AssertNothingDispatched(t) }

// AssertWorkflowDispatched fails when jobType was not workflow-dispatched.
// @group Testing
//
// Example: assert workflow dispatch by type
//
//	f := queuefake.New()
//	_, _ = f.Workflow().Chain(bus.NewJob("a", nil)).Dispatch(nil)
//	f.AssertWorkflowDispatched(t, "a")
func (f *Fake) AssertWorkflowDispatched(t testing.TB, jobType string) { f.b.AssertDispatched(t, jobType) }

// AssertWorkflowDispatchedOn fails when jobType was not workflow-dispatched on queueName.
// @group Testing
//
// Example: assert workflow dispatch on queue
//
//	f := queuefake.New()
//	_, _ = f.Workflow().Chain(bus.NewJob("a", nil)).OnQueue("critical").Dispatch(nil)
//	f.AssertWorkflowDispatchedOn(t, "critical", "a")
func (f *Fake) AssertWorkflowDispatchedOn(t testing.TB, queueName, jobType string) {
	f.b.AssertDispatchedOn(t, queueName, jobType)
}

// AssertWorkflowDispatchedTimes fails when workflow dispatch count for jobType does not match expected.
// @group Testing
//
// Example: assert workflow dispatch count
//
//	f := queuefake.New()
//	wf := f.Workflow()
//	_, _ = wf.Chain(bus.NewJob("a", nil)).Dispatch(nil)
//	_, _ = wf.Chain(bus.NewJob("a", nil)).Dispatch(nil)
//	f.AssertWorkflowDispatchedTimes(t, "a", 2)
func (f *Fake) AssertWorkflowDispatchedTimes(t testing.TB, jobType string, expected int) {
	f.b.AssertDispatchedTimes(t, jobType, expected)
}

// AssertWorkflowNotDispatched fails when jobType was workflow-dispatched.
// @group Testing
//
// Example: assert workflow not dispatched
//
//	f := queuefake.New()
//	f.AssertWorkflowNotDispatched(t, "emails:send")
func (f *Fake) AssertWorkflowNotDispatched(t testing.TB, jobType string) { f.b.AssertNotDispatched(t, jobType) }

// AssertChained fails if no recorded workflow chain matches expected job type order.
// @group Testing
//
// Example: assert chain sequence
//
//	f := queuefake.New()
//	_, _ = f.Workflow().Chain(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(nil)
//	f.AssertChained(t, []string{"a", "b"})
func (f *Fake) AssertChained(t testing.TB, expected []string) { f.b.AssertChained(t, expected) }

// AssertBatchCount fails if total recorded workflow batch count does not match n.
// @group Testing
//
// Example: assert workflow batch count
//
//	f := queuefake.New()
//	_, _ = f.Workflow().Batch(bus.NewJob("a", nil)).Dispatch(nil)
//	f.AssertBatchCount(t, 1)
func (f *Fake) AssertBatchCount(t testing.TB, n int) { f.b.AssertBatchCount(t, n) }

// AssertNothingBatched fails if any workflow batch was recorded.
// @group Testing
//
// Example: assert no workflow batches
//
//	f := queuefake.New()
//	f.AssertNothingBatched(t)
func (f *Fake) AssertNothingBatched(t testing.TB) { f.b.AssertNothingBatched(t) }

// AssertBatched fails unless at least one recorded workflow batch matches predicate.
// @group Testing
//
// Example: assert batched jobs by predicate
//
//	f := queuefake.New()
//	_, _ = f.Workflow().Batch(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(nil)
//	f.AssertBatched(t, func(spec bus.BatchSpec) bool { return len(spec.JobTypes) == 2 })
func (f *Fake) AssertBatched(t testing.TB, predicate func(spec bus.BatchSpec) bool) {
	f.b.AssertBatched(t, predicate)
}

// AssertNothingDispatched fails when any dispatch was recorded.
// @group Testing
//
// Example: assert no queue dispatches
//
//	f := queuefake.New()
//	f.AssertNothingDispatched(t)
func (f *Fake) AssertNothingDispatched(t testing.TB) { f.q.AssertNothingDispatched(t) }

// AssertCount fails when total dispatch count is not expected.
// @group Testing
//
// Example: assert total queue dispatch count
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("a"))
//	_ = q.Dispatch(queue.NewJob("b"))
//	f.AssertCount(t, 2)
func (f *Fake) AssertCount(t testing.TB, expected int) { f.q.AssertCount(t, expected) }

// AssertDispatched fails when jobType was not dispatched.
// @group Testing
//
// Example: assert queue dispatch by type
//
//	f := queuefake.New()
//	_ = f.Queue().Dispatch(queue.NewJob("emails:send"))
//	f.AssertDispatched(t, "emails:send")
func (f *Fake) AssertDispatched(t testing.TB, jobType string) { f.q.AssertDispatched(t, jobType) }

// AssertDispatchedOn fails when jobType was not dispatched on queueName.
// @group Testing
//
// Example: assert queue dispatch on queue
//
//	f := queuefake.New()
//	_ = f.Queue().Dispatch(queue.NewJob("emails:send").OnQueue("critical"))
//	f.AssertDispatchedOn(t, "critical", "emails:send")
func (f *Fake) AssertDispatchedOn(t testing.TB, queueName, jobType string) {
	f.q.AssertDispatchedOn(t, queueName, jobType)
}

// AssertDispatchedTimes fails when jobType dispatch count does not match expected.
// @group Testing
//
// Example: assert queue dispatch count by type
//
//	f := queuefake.New()
//	q := f.Queue()
//	_ = q.Dispatch(queue.NewJob("emails:send"))
//	_ = q.Dispatch(queue.NewJob("emails:send"))
//	f.AssertDispatchedTimes(t, "emails:send", 2)
func (f *Fake) AssertDispatchedTimes(t testing.TB, jobType string, expected int) {
	f.q.AssertDispatchedTimes(t, jobType, expected)
}

// AssertNotDispatched fails when jobType was dispatched.
// @group Testing
//
// Example: assert queue type was not dispatched
//
//	f := queuefake.New()
//	f.AssertNotDispatched(t, "emails:send")
func (f *Fake) AssertNotDispatched(t testing.TB, jobType string) { f.q.AssertNotDispatched(t, jobType) }
