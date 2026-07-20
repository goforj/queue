package queuefake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/queuefake"
)

func TestFakeHarness_QueueAndAssertions(t *testing.T) {
	f := queuefake.New()
	q := f.Queue()

	f.AssertNothingDispatched(t)

	if err := q.Dispatch(queue.NewJob("emails:send").OnQueue("default")); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	if err := q.Dispatch(queue.NewJob("emails:send").OnQueue("critical")); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if err := q.Dispatch(queue.NewJob("emails:cleanup").OnQueue("default")); err != nil {
		t.Fatalf("dispatch 3: %v", err)
	}

	if got := f.Count(); got != 3 {
		t.Fatalf("Count()=%d want 3", got)
	}
	if got := f.CountJob("emails:send"); got != 2 {
		t.Fatalf("CountJob(emails:send)=%d want 2", got)
	}
	if got := f.CountOn("critical", "emails:send"); got != 1 {
		t.Fatalf("CountOn(critical, emails:send)=%d want 1", got)
	}

	f.AssertCount(t, 3)
	f.AssertDispatched(t, "emails:send")
	f.AssertDispatchedOn(t, "critical", "emails:send")
	f.AssertDispatchedTimes(t, "emails:send", 2)
	f.AssertNotDispatched(t, "emails:archive")

	if got := len(f.Records()); got != 3 {
		t.Fatalf("Records len=%d want 3", got)
	}

	f.Reset()
	f.AssertNothingDispatched(t)
}

func TestFakeHarness_WorkflowAssertions(t *testing.T) {
	f := queuefake.New()
	wf := f.Workflow()

	f.AssertNothingWorkflowDispatched(t)
	f.AssertNothingBatched(t)

	_, _ = wf.Dispatch(context.Background(), bus.NewJob("reports:generate", nil).OnQueue("critical"))
	_, _ = wf.Dispatch(context.Background(), bus.NewJob("reports:generate", nil))
	_, _ = wf.Chain(
		bus.NewJob("a", nil),
		bus.NewJob("b", nil),
	).Dispatch(context.Background())
	_, _ = wf.Batch(
		bus.NewJob("x", nil),
		bus.NewJob("y", nil),
	).Dispatch(context.Background())

	f.AssertWorkflowDispatched(t, "reports:generate")
	f.AssertWorkflowDispatchedOn(t, "critical", "reports:generate")
	f.AssertWorkflowDispatchedTimes(t, "reports:generate", 2)
	f.AssertWorkflowNotDispatched(t, "reports:archive")
	f.AssertChained(t, []string{"a", "b"})
	f.AssertBatchCount(t, 1)
	f.AssertBatched(t, func(spec bus.BatchSpec) bool {
		return len(spec.JobTypes) == 2 && spec.JobTypes[0] == "x"
	})
}

// TestFakeHarness_CompatibilityViewsShareState verifies queuefake no longer
// owns independent queue and workflow recorder models.
func TestFakeHarness_CompatibilityViewsShareState(t *testing.T) {
	fake := queuefake.New()
	if fake.Queue() != fake.Workflow().Queue() {
		t.Fatal("Queue and Workflow returned different canonical fakes")
	}
	if err := fake.Queue().Dispatch(queue.NewJob("queue:direct").OnQueue("root")); err != nil {
		t.Fatalf("queue dispatch: %v", err)
	}
	fake.AssertWorkflowDispatched(t, "queue:direct")
	if _, err := fake.Workflow().Dispatch(context.Background(), bus.NewJob("workflow:direct", nil).OnQueue("legacy")); err != nil {
		t.Fatalf("workflow dispatch: %v", err)
	}
	fake.AssertDispatched(t, "workflow:direct")

	chainID, err := fake.Queue().Chain(queue.NewJob("chain:shared")).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("chain dispatch: %v", err)
	}
	batchID, err := fake.Workflow().Batch(bus.NewJob("batch:shared", nil)).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("batch dispatch: %v", err)
	}
	fake.AssertChained(t, []string{"chain:shared"})
	fake.AssertBatchCount(t, 1)

	fake.Reset()
	fake.AssertNothingDispatched(t)
	fake.AssertNothingWorkflowDispatched(t)
	fake.AssertNothingBatched(t)
	if len(fake.Queue().ChainRecords()) != 0 {
		t.Fatal("Reset retained chain records")
	}
	if _, err := fake.Queue().FindChain(context.Background(), chainID); !errors.Is(err, queue.ErrWorkflowNotFound) {
		t.Fatalf("FindChain after Reset error = %v", err)
	}
	if _, err := fake.Workflow().FindBatch(context.Background(), batchID); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("FindBatch after Reset error = %v", err)
	}
}
