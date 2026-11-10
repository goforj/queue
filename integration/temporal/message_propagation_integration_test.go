//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// nativeBoundaryHeaderKey selects a rejected header on one client call without changing workflow input.
type nativeBoundaryHeaderKey struct{}

// nativeBoundaryPropagator accepts initial workflow context and rejects only explicitly marked message headers.
type nativeBoundaryPropagator struct {
	unavailable atomic.Bool
	rejections  atomic.Int64
}

// nativeAvailabilityHeaderKey selects a recoverable adapter outage on one already-accepted signal.
type nativeAvailabilityHeaderKey struct{}

// MaxDecodedBytes keeps the fixture within the public bounded propagation contract.
func (*nativeBoundaryPropagator) MaxDecodedBytes() int { return 4096 }

// Inject emits a small rejection fixture only for a selected client call.
func (*nativeBoundaryPropagator) Inject(ctx context.Context, w workflow.HeaderWriter) error {
	if reject, _ := ctx.Value(nativeBoundaryHeaderKey{}).(bool); reject {
		w.Set("review-rejected", &common.Payload{Data: []byte("synthetic-rejected")})
	}
	if selected, _ := ctx.Value(nativeAvailabilityHeaderKey{}).(bool); selected {
		w.Set("fixture-availability", &common.Payload{Data: []byte("synthetic-availability")})
	}
	return nil
}

// InjectFromWorkflow keeps ordinary workflow activity propagation empty.
func (*nativeBoundaryPropagator) InjectFromWorkflow(workflow.Context, workflow.HeaderWriter) error {
	return nil
}

// Extract preserves ordinary activity contexts.
func (*nativeBoundaryPropagator) Extract(ctx context.Context, _ workflow.HeaderReader) (context.Context, error) {
	return ctx, nil
}

// ExtractToWorkflow rejects the synthetic message before any registered user handler should run.
func (p *nativeBoundaryPropagator) ExtractToWorkflow(ctx workflow.Context, r workflow.HeaderReader) (workflow.Context, error) {
	if _, reject := r.Get("review-rejected"); reject {
		return nil, errors.New("synthetic rejected message context")
	}
	if _, selected := r.Get("fixture-availability"); selected && p.unavailable.Load() {
		p.rejections.Add(1)
		return nil, temporalqueue.ErrAdapterUnavailable
	}
	return ctx, nil
}

// TestTemporalDriverRecoversUnavailableSignalPropagation retries the original signal task after adapter recovery without resubmission.
func TestTemporalDriverRecoversUnavailableSignalPropagation(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-signal-availability-%d", time.Now().UnixNano())
	propagator := &nativeBoundaryPropagator{}
	propagator.unavailable.Store(true)
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
		DriverBaseConfig:       queueconfig.DriverBaseConfig{DefaultQueue: name},
		ClientOptions:          client.Options{HostPort: temporalAddress, ContextPropagators: []workflow.ContextPropagator{propagator}},
		DefaultActivityTimeout: time.Second, ShutdownTimeout: 5 * time.Second,
		Register: func(registry worker.Registry) error { registry.RegisterWorkflow(nativeMessageWorkflow); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := q.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeMessageWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sdk.TerminateWorkflow(context.Background(), name, run.GetRunID(), "synthetic signal test cleanup")
	})
	if _, err := sdk.QueryWorkflow(ctx, name, run.GetRunID(), "total"); err != nil {
		t.Fatal(err)
	}
	selected := context.WithValue(ctx, nativeAvailabilityHeaderKey{}, true)
	if err := sdk.SignalWorkflow(selected, name, run.GetRunID(), "finish", true); err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool {
		iter := sdk.GetWorkflowHistory(ctx, name, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for iter.HasNext() {
			event, err := iter.Next()
			if err != nil {
				t.Fatal(err)
			}
			if event.GetWorkflowTaskFailedEventAttributes() != nil {
				return true
			}
		}
		return false
	})
	if propagator.rejections.Load() == 0 {
		t.Fatal("signal did not encounter adapter unavailability")
	}
	propagator.unavailable.Store(false)
	var result int
	if err := run.Get(ctx, &result); err != nil || result != 0 {
		t.Fatal("original signal failed to recover without resubmission", result, err)
	}
}

// TestTemporalDriverRejectsNativeMessageHeaders verifies rejection and valid message processing after physical worker restart.
func TestTemporalDriverRejectsNativeMessageHeaders(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-message-headers-%d", time.Now().UnixNano())
	cfg := temporalqueue.Config{
		DriverBaseConfig:       queueconfig.DriverBaseConfig{DefaultQueue: name},
		ClientOptions:          client.Options{HostPort: temporalAddress, ContextPropagators: []workflow.ContextPropagator{&nativeBoundaryPropagator{}}},
		DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second,
		Register: func(r worker.Registry) error { r.RegisterWorkflow(nativeMessageWorkflow); return nil },
	}
	q, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeMessageWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.QueryWorkflow(ctx, name, run.GetRunID(), "total"); err != nil {
		t.Fatal(err)
	}
	rejected := context.WithValue(ctx, nativeBoundaryHeaderKey{}, true)
	_, queryErr := sdk.QueryWorkflow(rejected, name, run.GetRunID(), "total")
	t.Logf("rejected-header query: error=%v", queryErr)
	update, updateErr := sdk.UpdateWorkflow(rejected, client.UpdateWorkflowOptions{WorkflowID: name, RunID: run.GetRunID(), UpdateName: "add", Args: []any{7}, WaitForStage: client.WorkflowUpdateStageCompleted})
	var updated int
	if updateErr == nil {
		updateErr = update.Get(ctx, &updated)
	}
	t.Logf("rejected-header update: value=%d error=%v", updated, updateErr)
	if queryErr == nil || updateErr == nil {
		t.Fatal("rejected message reached query or update handler")
	}
	if err := sdk.SignalWorkflow(rejected, name, run.GetRunID(), "finish", true); err != nil {
		t.Fatal(err)
	}
	query, err := sdk.QueryWorkflow(ctx, name, run.GetRunID(), "total")
	if err != nil {
		t.Fatal("invalid signal poisoned query processing", err)
	}
	var value int
	if err := query.Get(&value); err != nil || value != 0 {
		t.Fatalf("rejected update changed state: total=%d error=%v", value, err)
	}
	description, err := sdk.DescribeWorkflowExecution(ctx, name, run.GetRunID())
	if err != nil || description.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatal("rejected signal completed workflow", description, err)
	}
	if err := q.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(context.Background())
	if err := restarted.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk2, err := temporalqueue.Client(restarted)
	if err != nil {
		t.Fatal(err)
	}
	defer sdk2.TerminateWorkflow(context.Background(), name, run.GetRunID(), "synthetic message test cleanup")
	query, err = sdk2.QueryWorkflow(ctx, name, run.GetRunID(), "total")
	if err != nil {
		t.Fatal("invalid signal poisoned replay after restart", err)
	}
	if err := query.Get(&value); err != nil || value != 0 {
		t.Fatalf("replay state=%d error=%v", value, err)
	}
	replayer, err := temporalqueue.NewWorkflowReplayer(restarted)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayer.ReplayWorkflowExecution(ctx, sdk2.WorkflowService(), nil, "default", workflow.Execution{ID: name, RunID: run.GetRunID()}); err != nil {
		t.Fatal("rejected signal poisoned explicit SDK replay", err)
	}
	accepted, err := sdk2.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{WorkflowID: name, RunID: run.GetRunID(), UpdateName: "add", Args: []any{3}, WaitForStage: client.WorkflowUpdateStageCompleted})
	if err != nil {
		t.Fatal("valid update rejected after restart", err)
	}
	if err := accepted.Get(ctx, &updated); err != nil || updated != 3 {
		t.Fatalf("valid update=%d error=%v", updated, err)
	}
	if err := sdk2.SignalWorkflow(ctx, name, run.GetRunID(), "finish", true); err != nil {
		t.Fatal(err)
	}
	var final int
	if err := sdk2.GetWorkflow(ctx, name, run.GetRunID()).Get(ctx, &final); err != nil || final != 3 {
		t.Fatalf("valid signal after replay: result=%d error=%v", final, err)
	}
}
