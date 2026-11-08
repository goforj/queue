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
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"
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

	// The maximum reservation combines all supported members with four progress definitions.
	businessBefore, hooksBefore := business.Load(), hooks.Load()
	maximumJobs := make([]queue.Job, 256)
	for i := range maximumJobs {
		maximumJobs[i] = queue.NewJob("member").Retry(2147483646)
	}
	maximum := queue.Continue(q.Batch(maximumJobs...))
	for range 4 {
		maximum.OnProgress(queue.NewJob("hook").Retry(2147483646))
	}
	started := time.Now()
	maximumID, err := maximum.Dispatch(t.Context())
	acceptanceDuration := time.Since(started)
	if err != nil {
		t.Fatalf("maximum combined acceptance failed after %s: %v", acceptanceDuration, err)
	}
	t.Logf("maximum combined accepted workflow=%s members=256 definitions=4 invocations=1024 reserved_bytes=%d acceptance=%s", maximumID, 4<<20, acceptanceDuration)
	if err := c.GetWorkflow(t.Context(), maximumID, "").Get(t.Context(), nil); err != nil {
		t.Fatalf("maximum combined completion after %s: %v", time.Since(started), err)
	}
	completionDuration := time.Since(started)
	outcomeStarted := time.Now()
	maximumOutcomes, err := q.FindContinuations(t.Context(), maximumID)
	if err != nil {
		t.Fatalf("maximum combined continuation read: %v", err)
	}
	if len(maximumOutcomes) != 1024 {
		t.Fatalf("maximum combined outcomes=%d, want 1024", len(maximumOutcomes))
	}
	ids := make(map[string]bool, 1024)
	for _, outcome := range maximumOutcomes {
		if outcome.Status != "succeeded" {
			t.Fatalf("maximum combined outcome status=%s", outcome.Status)
		}
		if ids[outcome.Invocation.ID] {
			t.Fatal("duplicate maximum combined invocation identity")
		}
		ids[outcome.Invocation.ID] = true
	}
	maximumState, err := q.FindBatch(t.Context(), maximumID)
	if err != nil || !maximumState.Completed || maximumState.Processed != 256 || maximumState.Pending != 0 || maximumState.Failed != 0 {
		t.Fatalf("maximum combined business state %+v, error %v", maximumState, err)
	}
	if business.Load()-businessBefore != 256 || hooks.Load()-hooksBefore != 1024 {
		t.Fatalf("maximum combined actual handlers business=%d hooks=%d", business.Load()-businessBefore, hooks.Load()-hooksBefore)
	}
	iterator := c.GetWorkflowHistory(t.Context(), maximumID, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	recorded := &history.History{}
	counts := make(map[string]int)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		recorded.Events = append(recorded.Events, event)
		counts[event.EventType.String()]++
	}
	t.Logf("maximum combined PASS business=%d hooks=%d outcomes=%d reservation_bytes=%d history_bytes=%d events=%d completion=%s outcome_read_and_state=%s events_by_type=%v", business.Load()-businessBefore, hooks.Load()-hooksBefore, len(maximumOutcomes), 4<<20, proto.Size(recorded), len(recorded.Events), completionDuration, time.Since(outcomeStarted), counts)
}
