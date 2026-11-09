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

// initialReplayStateWorkflow supplies committed initial state without any command that could mask premature replay closure.
func initialReplayStateWorkflow(ctx workflow.Context) (int, error) {
	total := 7
	if err := workflow.SetQueryHandler(ctx, "total", func() (int, error) { return total, nil }); err != nil {
		return 0, err
	}
	var finish bool
	workflow.GetSignalChannel(ctx, "finish").Receive(ctx, &finish)
	return total, nil
}

// TestTemporalDriverAcceptedUpdateSurvivesReplayOutage retains a committed update through adapter failure and worker replacement.
func TestTemporalDriverAcceptedUpdateSurvivesReplayOutage(t *testing.T) {
	verifyNativeReplayAdapterRecovery(t, false)
}

// TestTemporalDriverAcceptedWorkflowSurvivesReplayOutage retains an existing workflow when its original-start headers cannot temporarily decode.
func TestTemporalDriverAcceptedWorkflowSurvivesReplayOutage(t *testing.T) {
	verifyNativeReplayAdapterRecovery(t, true)
}

// verifyNativeReplayAdapterRecovery prevents adapter failure from replacing committed native workflow state or outcomes.
func verifyNativeReplayAdapterRecovery(t *testing.T, startHeader bool) {
	requireTemporalServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	name := fmt.Sprintf("driver-native-replay-%t-%d", startHeader, time.Now().UnixNano())
	definition := nativeMessageWorkflow
	if startHeader {
		definition = initialReplayStateWorkflow
	}
	propagator := &nativeBoundaryPropagator{}
	cfg := temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, ContextPropagators: []workflow.ContextPropagator{propagator}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second, Register: func(r worker.Registry) error { r.RegisterWorkflow(definition); return nil }}
	first, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })
	if err := first.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	selected := context.WithValue(ctx, nativeAvailabilityHeaderKey{}, true)
	startCtx := ctx
	if startHeader {
		startCtx = selected
	}
	run, err := sdk.ExecuteWorkflow(startCtx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, definition)
	if err != nil {
		t.Fatal(err)
	}
	var value int
	if startHeader {
		query, err := sdk.QueryWorkflow(ctx, name, run.GetRunID(), "total")
		if err != nil {
			t.Fatal(err)
		}
		if err := query.Get(&value); err != nil || value != 7 {
			t.Fatal(value, err)
		}
	} else {
		update, err := sdk.UpdateWorkflow(selected, client.UpdateWorkflowOptions{WorkflowID: name, RunID: run.GetRunID(), UpdateName: "add", Args: []any{7}, WaitForStage: client.WorkflowUpdateStageCompleted})
		if err != nil {
			t.Fatal(err)
		}
		if err := update.Get(ctx, &value); err != nil || value != 7 {
			t.Fatal(value, err)
		}
	}

	committed := false
	original := sdk.GetWorkflowHistory(ctx, name, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for original.HasNext() {
		event, err := original.Next()
		if err != nil {
			t.Fatal(err)
		}
		if event.GetWorkflowTaskCompletedEventAttributes() != nil {
			committed = true
		}
	}
	if !committed {
		t.Fatal("fixture has no committed original workflow task to replay")
	}
	if err := first.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Shutdown(context.Background()) })
	sdk2, err := temporalqueue.Client(second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sdk2.TerminateWorkflow(context.Background(), name, run.GetRunID(), "synthetic native replay cleanup")
	})
	propagator.unavailable.Store(true)
	replay, err := temporalqueue.NewWorkflowReplayer(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayWorkflowExecution(ctx, sdk2.WorkflowService(), nil, "default", workflow.Execution{ID: name, RunID: run.GetRunID()}); err == nil {
		t.Fatal("replay accepted a changed accepted native state during propagation outage")
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
				t.Fatal("replacement worker did not fail the rejected native replay task", observed.Err())
			}
		}
	}
	description, err = sdk2.DescribeWorkflowExecution(ctx, name, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	if description.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatal("workflow closed during rejected native replay", description.WorkflowExecutionInfo.Status)
	}
	propagator.unavailable.Store(false)
	if err := sdk2.GetWorkflow(ctx, name, run.GetRunID()).Get(ctx, &value); err != nil {
		t.Fatal(err)
	}
	if value != 7 {
		t.Fatalf("accepted native state lost in successful workflow result: got=%d want=7", value)
	}
	if err := replay.ReplayWorkflowExecution(ctx, sdk2.WorkflowService(), nil, "default", workflow.Execution{ID: name, RunID: run.GetRunID()}); err != nil {
		t.Fatal("retained native history failed replay after adapter recovery", err)
	}
}
