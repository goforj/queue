//go:build integration

package temporal

import (
	"context"
	"fmt"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"testing"
	"time"
)

// TestTemporalDriverAcceptedUpdateSurvivesReplayOutage retains previously committed native update state across adapter outages.
func TestTemporalDriverAcceptedUpdateSurvivesReplayOutage(t *testing.T) {
	requireTemporalServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	name := fmt.Sprintf("driver-update-replay-%d", time.Now().UnixNano())
	propagator := &nativeBoundaryPropagator{}
	cfg := temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, ContextPropagators: []workflow.ContextPropagator{propagator}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second, Register: func(r worker.Registry) error { r.RegisterWorkflow(nativeMessageWorkflow); return nil }}
	first, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })
	if err := first.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, _ := temporalqueue.Client(first)
	run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeMessageWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	selected := context.WithValue(ctx, nativeAvailabilityHeaderKey{}, true)
	update, err := sdk.UpdateWorkflow(selected, client.UpdateWorkflowOptions{WorkflowID: name, RunID: run.GetRunID(), UpdateName: "add", Args: []any{7}, WaitForStage: client.WorkflowUpdateStageCompleted})
	if err != nil {
		t.Fatal(err)
	}
	var value int
	if err := update.Get(ctx, &value); err != nil || value != 7 {
		t.Fatal(value, err)
	}
	if err := first.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Shutdown(context.Background()) })
	sdk2, _ := temporalqueue.Client(second)
	t.Cleanup(func() {
		_ = sdk2.TerminateWorkflow(context.Background(), name, run.GetRunID(), "synthetic update replay cleanup")
	})
	propagator.unavailable.Store(true)
	replay, err := temporalqueue.NewWorkflowReplayer(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayWorkflowExecution(ctx, sdk2.WorkflowService(), nil, "default", workflow.Execution{ID: name, RunID: run.GetRunID()}); err == nil {
		t.Fatal("replay accepted a skipped committed update during propagation outage")
	}
	description, err := sdk2.DescribeWorkflowExecution(ctx, name, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	watermark := description.WorkflowExecutionInfo.HistoryLength
	if err := second.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sdk2.SignalWorkflow(ctx, name, run.GetRunID(), "finish", true); err != nil {
		t.Fatal(err)
	}
	observed, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	failedTask := false
	for !failedTask {
		history := sdk2.GetWorkflowHistory(observed, name, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for history.HasNext() {
			event, err := history.Next()
			if err != nil {
				t.Fatal(err)
			}
			if event.EventId > watermark && event.GetWorkflowTaskFailedEventAttributes() != nil {
				failedTask = true
			}
		}
		if !failedTask {
			select {
			case <-tick.C:
			case <-observed.Done():
				t.Fatal("replacement worker did not fail the rejected update replay task", observed.Err())
			}
		}
	}
	description, err = sdk2.DescribeWorkflowExecution(ctx, name, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	if description.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatal("workflow closed during rejected update replay", description.WorkflowExecutionInfo.Status)
	}
	propagator.unavailable.Store(false)
	if err := sdk2.GetWorkflow(ctx, name, run.GetRunID()).Get(ctx, &value); err != nil {
		t.Fatal(err)
	}
	if value != 7 {
		t.Fatalf("committed update state lost in successful workflow result: got=%d want=7", value)
	}
	if err := replay.ReplayWorkflowExecution(ctx, sdk2.WorkflowService(), nil, "default", workflow.Execution{ID: name, RunID: run.GetRunID()}); err != nil {
		t.Fatal("retained update history failed replay after adapter recovery", err)
	}
}
