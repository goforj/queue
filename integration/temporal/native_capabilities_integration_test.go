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
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// nativeApprovalResult carries durable workflow state across worker replacement.
type nativeApprovalResult struct {
	Value    int
	Approved bool
}

// nativeApprovalWorkflow combines a durable timer, query, validated update and signal without application I/O.
func nativeApprovalWorkflow(ctx workflow.Context) (nativeApprovalResult, error) {
	state := nativeApprovalResult{}
	if err := workflow.SetQueryHandler(ctx, "status", func() (nativeApprovalResult, error) { return state, nil }); err != nil {
		return state, err
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, "amount", func(_ workflow.Context, value int) (int, error) { state.Value = value; return value, nil }, workflow.UpdateHandlerOptions{Validator: func(value int) error {
		if value <= 0 {
			return errors.New("amount must be positive")
		}
		return nil
	}}); err != nil {
		return state, err
	}
	timerCtx, cancel := workflow.WithCancel(ctx)
	timer := workflow.NewTimer(timerCtx, time.Hour)
	selector := workflow.NewSelector(ctx)
	selector.AddReceive(workflow.GetSignalChannel(ctx, "approve"), func(channel workflow.ReceiveChannel, _ bool) { channel.Receive(ctx, &state.Approved) })
	selector.AddFuture(timer, func(workflow.Future) {})
	selector.Select(ctx)
	cancel()
	return state, nil
}

// TestTemporalDriverApprovalSurvivesRestart exercises the complete native approval scenario through managed resources.
func TestTemporalDriverApprovalSurvivesRestart(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("native-approval-%d", time.Now().UnixNano())
	register := func(r worker.Registry) error { r.RegisterWorkflow(nativeApprovalWorkflow); return nil }
	first := newDriverResource(t, name, register)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := first.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeApprovalWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	update, err := sdk.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{WorkflowID: name, UpdateName: "amount", Args: []any{7}, WaitForStage: client.WorkflowUpdateStageCompleted})
	if err != nil {
		t.Fatal(err)
	}
	var amount int
	if err := update.Get(ctx, &amount); err != nil || amount != 7 {
		t.Fatal(amount, err)
	}
	rejected, err := sdk.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{WorkflowID: name, UpdateName: "amount", Args: []any{-1}, WaitForStage: client.WorkflowUpdateStageCompleted})
	if err == nil {
		err = rejected.Get(ctx, &amount)
	}
	if err == nil {
		t.Fatal("invalid native update accepted")
	}
	if err := first.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	second := newDriverResource(t, name, register)
	if err := second.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err = temporalqueue.Client(second)
	if err != nil {
		t.Fatal(err)
	}
	query, err := sdk.QueryWorkflow(ctx, name, run.GetRunID(), "status")
	if err != nil {
		t.Fatal(err)
	}
	var state nativeApprovalResult
	if err := query.Get(&state); err != nil || state.Value != 7 {
		t.Fatal(state, err)
	}
	if err := sdk.SignalWorkflow(ctx, name, run.GetRunID(), "approve", true); err != nil {
		t.Fatal(err)
	}
	if err := sdk.GetWorkflow(ctx, name, run.GetRunID()).Get(ctx, &state); err != nil || state != (nativeApprovalResult{Value: 7, Approved: true}) {
		t.Fatal(state, err)
	}
	started, cancelled := 0, 0
	history := sdk.GetWorkflowHistory(ctx, name, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for history.HasNext() {
		event, err := history.Next()
		if err != nil {
			t.Fatal(err)
		}
		if event.GetTimerStartedEventAttributes() != nil {
			started++
		}
		if event.GetTimerCanceledEventAttributes() != nil {
			cancelled++
		}
	}
	if started != 1 || cancelled != 1 {
		t.Fatal("durable approval timer not preserved", started, cancelled)
	}
}

// nativeCheckpointResult exposes the service-supplied attempt and recovered checkpoint.
type nativeCheckpointResult struct {
	Value   int
	Attempt int32
}

// nativeCheckpointWorkflow leaves retry and heartbeat delivery to the actual SDK activity path.
func nativeCheckpointWorkflow(ctx workflow.Context) (nativeCheckpointResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second, HeartbeatTimeout: 3 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second}})
	var result nativeCheckpointResult
	err := workflow.ExecuteActivity(ctx, "native-checkpoint").Get(ctx, &result)
	return result, err
}

// TestTemporalDriverHeartbeatRecovery preserves a service-recorded checkpoint across activity-worker replacement.
func TestTemporalDriverHeartbeatRecovery(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("native-checkpoint-%d", time.Now().UnixNano())
	entered := make(chan struct{})
	handler := func(ctx context.Context) (nativeCheckpointResult, error) {
		info := activity.GetInfo(ctx)
		if activity.HasHeartbeatDetails(ctx) {
			var value int
			if err := activity.GetHeartbeatDetails(ctx, &value); err != nil {
				return nativeCheckpointResult{}, err
			}
			return nativeCheckpointResult{Value: value + 1, Attempt: info.Attempt}, nil
		}
		if info.Attempt != 1 {
			return nativeCheckpointResult{}, temporal.NewNonRetryableApplicationError("checkpoint missing", "checkpoint-missing", nil)
		}
		activity.RecordHeartbeat(ctx, 41)
		close(entered)
		<-activity.GetWorkerStopChannel(ctx)
		return nativeCheckpointResult{}, temporal.NewApplicationError("retry after worker replacement", "checkpoint-retry")
	}
	register := func(r worker.Registry) error {
		r.RegisterWorkflow(nativeCheckpointWorkflow)
		r.RegisterActivityWithOptions(handler, activity.RegisterOptions{Name: "native-checkpoint"})
		return nil
	}
	first := newDriverResource(t, name, register)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := first.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeCheckpointWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitDriverState(t, func() bool {
		description, err := sdk.DescribeWorkflowExecution(ctx, name, run.GetRunID())
		return err == nil && len(description.PendingActivities) == 1 && description.PendingActivities[0].HeartbeatDetails != nil
	})
	if err := first.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	second := newDriverResource(t, name, register)
	if err := second.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err = temporalqueue.Client(second)
	if err != nil {
		t.Fatal(err)
	}
	var result nativeCheckpointResult
	if err := sdk.GetWorkflow(ctx, name, run.GetRunID()).Get(ctx, &result); err != nil || result != (nativeCheckpointResult{Value: 42, Attempt: 2}) {
		t.Fatal(result, err)
	}
}

// nativeAsyncWorkflow waits for application-owned SDK asynchronous completion.
func nativeAsyncWorkflow(ctx workflow.Context) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var result string
	err := workflow.ExecuteActivity(ctx, "native-async").Get(ctx, &result)
	return result, err
}

// TestTemporalDriverAsyncCompletion uses a separate resource to complete an activity after its original worker shuts down.
func TestTemporalDriverAsyncCompletion(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("native-async-%d", time.Now().UnixNano())
	tokens := make(chan []byte, 1)
	handler := func(ctx context.Context) (string, error) {
		tokens <- append([]byte(nil), activity.GetInfo(ctx).TaskToken...)
		return "", activity.ErrResultPending
	}
	register := func(r worker.Registry) error {
		r.RegisterWorkflow(nativeAsyncWorkflow)
		r.RegisterActivityWithOptions(handler, activity.RegisterOptions{Name: "native-async"})
		return nil
	}
	first := newDriverResource(t, name, register)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := first.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeAsyncWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var token []byte
	select {
	case token = <-tokens:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := first.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	second := newDriverResource(t, name, register)
	sdk, err = temporalqueue.Client(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.CompleteActivity(ctx, token, "completed externally", nil); err != nil {
		t.Fatal(err)
	}
	if err := second.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := sdk.GetWorkflow(ctx, name, run.GetRunID()).Get(ctx, &result); err != nil || result != "completed externally" {
		t.Fatal(result, err)
	}
}

// nativeReservation is a typed activity result that determines whether compensation is needed.
type nativeReservation struct {
	ID          string
	Approved    bool
	Compensated bool
}

// nativeCompensationWorkflow makes its compensation decision from a recorded activity result.
func nativeCompensationWorkflow(ctx workflow.Context, approved bool) (nativeReservation, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var result nativeReservation
	if err := workflow.ExecuteActivity(ctx, "native-reserve", approved).Get(ctx, &result); err != nil {
		return result, err
	}
	if !result.Approved {
		if err := workflow.ExecuteActivity(ctx, "native-release", result.ID).Get(ctx, nil); err != nil {
			return result, err
		}
		result.Compensated = true
	}
	return result, nil
}

// TestTemporalDriverTypedResultCompensation exercises both branches of actual native activity-result handling.
func TestTemporalDriverTypedResultCompensation(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("native-compensation-%d", time.Now().UnixNano())
	var releases atomic.Int32
	q := newDriverResource(t, name, func(r worker.Registry) error {
		r.RegisterWorkflow(nativeCompensationWorkflow)
		r.RegisterActivityWithOptions(func(_ context.Context, approved bool) (nativeReservation, error) {
			return nativeReservation{ID: "reservation", Approved: approved}, nil
		}, activity.RegisterOptions{Name: "native-reserve"})
		r.RegisterActivityWithOptions(func(_ context.Context, id string) error {
			if id != "reservation" {
				return errors.New("wrong compensation identity")
			}
			releases.Add(1)
			return nil
		}, activity.RegisterOptions{Name: "native-release"})
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, approved := range []bool{true, false} {
		run, err := sdk.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: fmt.Sprintf("%s-%t", name, approved), TaskQueue: name}, nativeCompensationWorkflow, approved)
		if err != nil {
			t.Fatal(err)
		}
		var result nativeReservation
		if err := run.Get(t.Context(), &result); err != nil || result != (nativeReservation{ID: "reservation", Approved: approved, Compensated: !approved}) {
			t.Fatal(result, err)
		}
	}
	if releases.Load() != 1 {
		t.Fatal("compensation executed for the wrong branch", releases.Load())
	}
}

// nativeCancellableChild returns native cancellation after its parent requests it.
func nativeCancellableChild(ctx workflow.Context) error {
	return workflow.Await(ctx, func() bool { return false })
}

// nativeChildPolicyWorkflow explicitly selects cancellation or termination behavior for its child.
func nativeChildPolicyWorkflow(ctx workflow.Context, id string, cancelChild bool) error {
	childCtx, cancel := workflow.WithCancel(ctx)
	policy := enums.PARENT_CLOSE_POLICY_TERMINATE
	if cancelChild {
		policy = enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL
	}
	childCtx = workflow.WithChildOptions(childCtx, workflow.ChildWorkflowOptions{WorkflowID: id, ParentClosePolicy: policy, WaitForCancellation: true})
	future := workflow.ExecuteChildWorkflow(childCtx, nativeCancellableChild)
	if err := future.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
		return err
	}
	if cancelChild {
		cancel()
		err := future.Get(ctx, nil)
		if !temporal.IsCanceledError(err) {
			return fmt.Errorf("child cancellation was not observed: %w", err)
		}
	}
	return nil
}

// TestTemporalDriverChildLifecyclePolicies proves service-recorded cancellation and parent-close termination.
func TestTemporalDriverChildLifecyclePolicies(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("native-child-policy-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, func(r worker.Registry) error {
		r.RegisterWorkflow(nativeChildPolicyWorkflow)
		r.RegisterWorkflow(nativeCancellableChild)
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelChild := range []bool{true, false} {
		childID := fmt.Sprintf("%s-child-%t", name, cancelChild)
		run, err := sdk.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: fmt.Sprintf("%s-parent-%t", name, cancelChild), TaskQueue: name}, nativeChildPolicyWorkflow, childID, cancelChild)
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Get(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		want := enums.WORKFLOW_EXECUTION_STATUS_TERMINATED
		if cancelChild {
			want = enums.WORKFLOW_EXECUTION_STATUS_CANCELED
		}
		waitDriverState(t, func() bool {
			description, err := sdk.DescribeWorkflowExecution(t.Context(), childID, "")
			return err == nil && description.WorkflowExecutionInfo.Status == want
		})
	}
}
