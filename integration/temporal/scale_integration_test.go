//go:build integration

package temporal

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// sharedCapacityWorkflow schedules native work beside portable members on the same remote role.
func sharedCapacityWorkflow(ctx workflow.Context) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
	futures := make([]workflow.Future, 8)
	for i := range futures {
		futures[i] = workflow.ExecuteActivity(ctx, "capacity-native")
	}
	for _, future := range futures {
		if err := future.Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// TestTemporalDriverSharedRemoteCapacity verifies native and portable activity execution use one SDK capacity.
func TestTemporalDriverSharedRemoteCapacity(t *testing.T) {
	requireTemporalServer(t)
	var active, maximum atomic.Int32
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	work := func() {
		current := active.Add(1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
	}
	q := newDriverResource(t, fmt.Sprintf("driver-capacity-%d", time.Now().UnixNano()), func(r worker.Registry) error {
		r.RegisterWorkflow(sharedCapacityWorkflow)
		r.RegisterActivityWithOptions(func(context.Context) error { work(); return nil }, activity.RegisterOptions{Name: "capacity-native"})
		return nil
	})
	q.Register("capacity", func(context.Context, queue.Message) error { work(); return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs := make([]queue.Job, 8)
	for i := range jobs {
		jobs[i] = queue.NewJob("capacity")
	}
	id, err := q.Batch(jobs...).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := temporalqueue.Client(q)
	routing, _ := temporalqueue.WorkerRouting(q)
	native, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: fmt.Sprintf("native-capacity-%d", time.Now().UnixNano()), TaskQueue: routing.WorkflowTaskQueue}, sharedCapacityWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("remote workers did not fill shared capacity")
		}
	}
	close(release)
	if err := native.Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 4 {
		t.Fatalf("combined capacity=%d, want 4", maximum.Load())
	}
}

// TestTemporalDriverScaleAndPaging executes the largest member count and maximum definition count with boundary rejection.
func TestTemporalDriverScaleAndPaging(t *testing.T) {
	requireTemporalServer(t)
	q := newDriverResource(t, fmt.Sprintf("driver-scale-%d", time.Now().UnixNano()), nil)
	var business, hooks atomic.Int32
	q.Register("member", func(context.Context, queue.Message) error { business.Add(1); return nil })
	q.Register("hook", func(context.Context, queue.Message) error { hooks.Add(1); return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs := make([]queue.Job, 256)
	for i := range jobs {
		jobs[i] = queue.NewJob("member")
	}
	id, err := q.Batch(jobs...).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := temporalqueue.Client(q)
	if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	st, err := q.FindBatch(t.Context(), id)
	if err != nil || st.Processed != 256 || st.Pending != 0 || business.Load() != 256 {
		t.Fatalf("paged state %+v %v", st, err)
	}
	if _, err := q.Batch(append(jobs, queue.NewJob("member"))...).Dispatch(t.Context()); err == nil {
		t.Fatal("257 members accepted")
	}
	bound := queue.Continue(q.Chain(queue.NewJob("member")))
	for range 32 {
		bound.OnCompletion(queue.NewJob("hook"))
	}
	hookID, err := bound.Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(t.Context(), hookID, "").Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	outcomes, err := q.FindContinuations(t.Context(), hookID)
	if err != nil || len(outcomes) != 32 || hooks.Load() != 32 {
		t.Fatalf("max definitions %+v %v", outcomes, err)
	}
	bound.OnCompletion(queue.NewJob("hook"))
	if _, err := bound.Dispatch(t.Context()); err == nil {
		t.Fatal("33 hooks accepted")
	}
	multiplied := queue.Continue(q.Batch(jobs...))
	for range 5 {
		multiplied.OnProgress(queue.NewJob("hook"))
	}
	if _, err := multiplied.Dispatch(t.Context()); err == nil {
		t.Fatal("multiplication over history reservation accepted")
	}
}
