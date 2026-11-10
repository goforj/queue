//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// nativeMessageWorkflow exposes validated updates, a query and a terminating signal through actual SDK APIs.
func nativeMessageWorkflow(ctx workflow.Context) (int, error) {
	total := 0
	if err := workflow.SetQueryHandler(ctx, "total", func() (int, error) { return total, nil }); err != nil {
		return 0, err
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, "add", func(_ workflow.Context, value int) (int, error) { total += value; return total, nil }, workflow.UpdateHandlerOptions{Validator: func(value int) error {
		if value <= 0 {
			return fmt.Errorf("value must be positive")
		}
		return nil
	}}); err != nil {
		return 0, err
	}
	var finish bool
	workflow.GetSignalChannel(ctx, "finish").Receive(ctx, &finish)
	return total, nil
}

// nativeChildWorkflow returns a typed result without a queue adapter.
func nativeChildWorkflow(_ workflow.Context, value int) (int, error) { return value * 2, nil }

// nativeRolloverWorkflow proves child results, durable timers, version markers and continue-as-new coexist.
func nativeRolloverWorkflow(ctx workflow.Context, phase int) (int, error) {
	if phase == 0 {
		return 0, workflow.NewContinueAsNewError(ctx, nativeRolloverWorkflow, 1)
	}
	_ = workflow.GetVersion(ctx, "driver-native-v1", workflow.DefaultVersion, 1)
	if err := workflow.Sleep(ctx, time.Millisecond); err != nil {
		return 0, err
	}
	var result int
	err := workflow.ExecuteChildWorkflow(ctx, nativeChildWorkflow, 21).Get(ctx, &result)
	return result, err
}

// TestTemporalDriverNativeMessagesAndRollover exercises native APIs beyond portable queue semantics.
func TestTemporalDriverNativeMessagesAndRollover(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-native-messages-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, func(reg worker.Registry) error {
		reg.RegisterWorkflow(nativeMessageWorkflow)
		reg.RegisterWorkflow(nativeChildWorkflow)
		reg.RegisterWorkflow(nativeRolloverWorkflow)
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	start := c.NewWithStartWorkflowOperation(client.StartWorkflowOptions{ID: name, TaskQueue: name, WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING}, nativeMessageWorkflow)
	update, err := c.UpdateWithStartWorkflow(t.Context(), client.UpdateWithStartWorkflowOptions{StartWorkflowOperation: start, UpdateOptions: client.UpdateWorkflowOptions{UpdateName: "add", Args: []any{7}, WaitForStage: client.WorkflowUpdateStageCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	var total int
	if err := update.Get(t.Context(), &total); err != nil || total != 7 {
		t.Fatalf("update result %d %v", total, err)
	}
	rejected, err := c.UpdateWorkflow(t.Context(), client.UpdateWorkflowOptions{WorkflowID: name, UpdateName: "add", Args: []any{-1}, WaitForStage: client.WorkflowUpdateStageCompleted})
	if err == nil {
		err = rejected.Get(t.Context(), &total)
	}
	if err == nil {
		t.Fatal("invalid update was accepted")
	}
	query, err := c.QueryWorkflow(t.Context(), name, "", "total")
	if err != nil {
		t.Fatal(err)
	}
	if err := query.Get(&total); err != nil || total != 7 {
		t.Fatalf("query result %d %v", total, err)
	}
	if err := c.SignalWorkflow(t.Context(), name, "", "finish", true); err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(t.Context(), name, "").Get(t.Context(), &total); err != nil || total != 7 {
		t.Fatalf("signal result %d %v", total, err)
	}
	rollover, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name + "-rollover", TaskQueue: name}, nativeRolloverWorkflow, 0)
	if err != nil {
		t.Fatal(err)
	}
	var result int
	if err := rollover.Get(t.Context(), &result); err != nil || result != 42 {
		t.Fatalf("rollover/child result %d %v", result, err)
	}
}

// TestTemporalDriverShutdownRetainsResistantCallback proves a deadline does not authorize dependency cleanup.
func TestTemporalDriverShutdownRetainsResistantCallback(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-shutdown-%d", time.Now().UnixNano())
	binding, err := temporalqueue.NewClientBinding(client.Options{HostPort: temporalAddress}, temporalqueue.SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	activity := func(context.Context) error { close(entered); <-release; close(returned); return nil }
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{Binding: binding, DefaultActivityTimeout: time.Second, ShutdownTimeout: 100 * time.Millisecond, WorkerOptions: worker.Options{WorkerStopTimeout: 50 * time.Millisecond}, Register: func(reg worker.Registry) error {
		reg.RegisterActivityWithOptions(activity, activityOptions(name))
		reg.RegisterWorkflow(nativeResistantWorkflow)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = q.Shutdown(context.Background())
		_ = binding.Close(context.Background())
	}()
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name, TaskQueue: "default"}, nativeResistantWorkflow, name); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("resistant activity did not enter")
	}
	started := time.Now()
	if err := q.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown result %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("shutdown ignored resource deadline")
	}
	if err := binding.Close(t.Context()); err == nil {
		t.Fatal("binding closed while queue callback was active")
	}
	if _, err := binding.Client().CheckHealth(t.Context(), &client.CheckHealthRequest{}); err != nil {
		t.Fatal("borrowed binding was closed", err)
	}
	close(release)
	<-returned
	waitDriverState(t, func() bool { return q.Shutdown(context.Background()) == nil })
	if err := binding.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// activityOptions assigns an explicit SDK activity name to a closure used only in this test.
func activityOptions(name string) activity.RegisterOptions {
	return activity.RegisterOptions{Name: name}
}

// nativeResistantWorkflow waits on a native activity whose callback lifetime exceeds SDK Stop.
func nativeResistantWorkflow(ctx workflow.Context, name string) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	return workflow.ExecuteActivity(ctx, name).Get(ctx, nil)
}

// nativeSplitWorkflow persists its explicit remote route while keeping local activities on the workflow role.
func nativeSplitWorkflow(ctx workflow.Context, activityQueue string) (string, error) {
	remote := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: activityQueue, StartToCloseTimeout: 5 * time.Second})
	var first string
	if err := workflow.ExecuteActivity(remote, nativeDriverActivity).Get(ctx, &first); err != nil {
		return "", err
	}
	local := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: 5 * time.Second})
	var second string
	if err := workflow.ExecuteLocalActivity(local, nativeDriverActivity).Get(ctx, &second); err != nil {
		return "", err
	}
	return first + " / " + second, nil
}

// TestTemporalDriverSplitRoles exercises portable jobs, coordinator state and both native activity forms.
func TestTemporalDriverSplitRoles(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-split-%d", time.Now().UnixNano())
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress},
		WorkflowTaskQueue: name + "-workflows", ActivityTaskQueue: name + "-activities", DefaultActivityTimeout: 5 * time.Second, ShutdownTimeout: 5 * time.Second,
		Register: func(reg worker.Registry) error {
			reg.RegisterWorkflow(nativeSplitWorkflow)
			reg.RegisterActivity(nativeDriverActivity)
			return nil
		},
	}, queue.WithWorkers(2))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	q.Register("business", func(context.Context, queue.Message) error { return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := q.WithContext(t.Context()).Dispatch(queue.NewJob("business")); err != nil {
		t.Fatal(err)
	}
	id, err := queue.Continue(q.Chain(queue.NewJob("business"))).OnCompletion(queue.NewJob("business")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool {
		outcomes, err := q.FindContinuations(t.Context(), id)
		return err == nil && len(outcomes) == 1 && outcomes[0].Status == "succeeded"
	})
	routing, err := temporalqueue.WorkerRouting(q)
	if err != nil {
		t.Fatal(err)
	}
	c, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name, TaskQueue: routing.WorkflowTaskQueue}, nativeSplitWorkflow, routing.ActivityTaskQueue)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	if err := run.Get(t.Context(), &result); err != nil || result != "native result / native result" {
		t.Fatalf("split remote/local result %q %v", result, err)
	}
}

// nativePinnedWorkflow remains open across a deployment rollout and rollback.
func nativePinnedWorkflow(ctx workflow.Context) error {
	channel := workflow.GetSignalChannel(ctx, "step")
	for {
		var done bool
		channel.Receive(ctx, &done)
		if done {
			return nil
		}
	}
}

// nativeAutoWorkflow receives the same events under auto-upgrade deployment routing.
func nativeAutoWorkflow(ctx workflow.Context) error {
	channel := workflow.GetSignalChannel(ctx, "step")
	for {
		var done bool
		channel.Receive(ctx, &done)
		if done {
			return nil
		}
	}
}

// TestTemporalDriverDeploymentRolloutAndRollback preserves pinned and auto-upgrade SDK registration policies.
func TestTemporalDriverDeploymentRolloutAndRollback(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-deployment-%d", time.Now().UnixNano())
	register := func(reg worker.Registry) error {
		reg.RegisterWorkflowWithOptions(nativePinnedWorkflow, workflow.RegisterOptions{VersioningBehavior: workflow.VersioningBehaviorPinned})
		reg.RegisterWorkflowWithOptions(nativeAutoWorkflow, workflow.RegisterOptions{VersioningBehavior: workflow.VersioningBehaviorAutoUpgrade})
		return nil
	}
	options := worker.Options{DeploymentOptions: worker.DeploymentOptions{UseVersioning: true, Version: worker.WorkerDeploymentVersion{DeploymentName: name, BuildID: "a"}, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}}
	first := newDriverResource(t, name, register, options)
	if err := first.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	deployment := c.WorkerDeploymentClient().GetHandle(name)
	waitDriverState(t, func() bool {
		_, err := deployment.SetCurrentVersion(t.Context(), client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "a"})
		return err == nil
	})
	pinned, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name + "-pinned", TaskQueue: name}, nativePinnedWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	auto, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name + "-auto", TaskQueue: name}, nativeAutoWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	version := func(run client.WorkflowRun, want string, behavior enums.VersioningBehavior) bool {
		description, err := c.DescribeWorkflowExecution(t.Context(), run.GetID(), run.GetRunID())
		if err != nil {
			return false
		}
		info := description.GetWorkflowExecutionInfo().GetVersioningInfo()
		return info.GetBehavior() == behavior && info.GetDeploymentVersion().GetBuildId() == want
	}
	waitDriverState(t, func() bool {
		return version(pinned, "a", enums.VERSIONING_BEHAVIOR_PINNED) && version(auto, "a", enums.VERSIONING_BEHAVIOR_AUTO_UPGRADE)
	})
	options.DeploymentOptions.Version.BuildID = "b"
	second := newDriverResource(t, name, register, options)
	if err := second.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool {
		_, err := deployment.SetCurrentVersion(t.Context(), client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "b"})
		return err == nil
	})
	for _, run := range []client.WorkflowRun{pinned, auto} {
		if err := c.SignalWorkflow(t.Context(), run.GetID(), run.GetRunID(), "step", false); err != nil {
			t.Fatal(err)
		}
	}
	// Deployment routing propagates asynchronously; additional signals create tasks until Describe reports convergence.
	waitDriverState(t, func() bool {
		if version(pinned, "a", enums.VERSIONING_BEHAVIOR_PINNED) && version(auto, "b", enums.VERSIONING_BEHAVIOR_AUTO_UPGRADE) {
			return true
		}
		if err := c.SignalWorkflow(t.Context(), auto.GetID(), auto.GetRunID(), "step", false); err != nil {
			t.Fatal(err)
		}
		return false
	})
	if _, err := deployment.SetCurrentVersion(t.Context(), client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "a"}); err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool {
		if version(auto, "a", enums.VERSIONING_BEHAVIOR_AUTO_UPGRADE) {
			return true
		}
		if err := c.SignalWorkflow(t.Context(), auto.GetID(), auto.GetRunID(), "step", false); err != nil {
			t.Fatal(err)
		}
		return false
	})
	for _, run := range []client.WorkflowRun{pinned, auto} {
		if err := c.SignalWorkflow(t.Context(), run.GetID(), run.GetRunID(), "step", true); err != nil {
			t.Fatal(err)
		}
		if err := run.Get(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if !version(auto, "a", enums.VERSIONING_BEHAVIOR_AUTO_UPGRADE) {
		t.Fatal("auto-upgrade workflow did not follow rollback")
	}
}

// nativeGracefulResultWorkflow exposes completion encoding during actual SDK shutdown.
func nativeGracefulResultWorkflow(ctx workflow.Context) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var result string
	err := workflow.ExecuteActivity(ctx, "graceful-result").Get(ctx, &result)
	return result, err
}

// TestTemporalDriverShutdownPreservesTypedCompletion keeps adapters available through admitted SDK completion.
func TestTemporalDriverShutdownPreservesTypedCompletion(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-graceful-result-%d", time.Now().UnixNano())
	entered := make(chan struct{})
	q := newDriverResource(t, name, func(r worker.Registry) error {
		r.RegisterWorkflow(nativeGracefulResultWorkflow)
		r.RegisterActivityWithOptions(func(ctx context.Context) (string, error) {
			close(entered)
			<-activity.GetWorkerStopChannel(ctx)
			return "successful-result", nil
		}, activity.RegisterOptions{Name: "graceful-result"})
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, _ := temporalqueue.Client(q)
	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeGracefulResultWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("native activity never entered")
	}
	if err := q.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement := newDriverResource(t, name, func(r worker.Registry) error {
		r.RegisterWorkflow(nativeGracefulResultWorkflow)
		r.RegisterActivityWithOptions(func(context.Context) (string, error) {
			t.Error("completed native activity was repeated")
			return "unexpected-retry", nil
		}, activity.RegisterOptions{Name: "graceful-result"})
		return nil
	})
	if err := replacement.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, _ := temporalqueue.Client(replacement)
	var result string
	if err := reader.GetWorkflow(t.Context(), run.GetID(), run.GetRunID()).Get(t.Context(), &result); err != nil || result != "successful-result" {
		t.Fatalf("graceful result %q, error %v", result, err)
	}
}

// stopAwareClientInterceptor holds an inherited activity callback after the resource admits it.
type stopAwareClientInterceptor struct {
	interceptor.InterceptorBase
	entered chan struct{}
	once    sync.Once
}

// stopAwareInbound waits for graceful stop before forwarding to the remaining SDK activity pipeline.
type stopAwareInbound struct {
	interceptor.ActivityInboundInterceptorBase
	owner *stopAwareClientInterceptor
}

// InterceptActivity places the barrier between client-level admission and worker-level interceptors.
func (i *stopAwareClientInterceptor) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &stopAwareInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, owner: i}
}

// ExecuteActivity retains admitted ownership across the shutdown entry fence.
func (i *stopAwareInbound) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	i.owner.once.Do(func() { close(i.owner.entered) })
	<-activity.GetWorkerStopChannel(ctx)
	return i.Next.ExecuteActivity(ctx, in)
}

// TestTemporalDriverShutdownDrainsInheritedActivityInterceptor prevents nested admission from rejecting an already accepted callback.
func TestTemporalDriverShutdownDrainsInheritedActivityInterceptor(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-inherited-drain-%d", time.Now().UnixNano())
	barrier := &stopAwareClientInterceptor{entered: make(chan struct{})}
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, Interceptors: []interceptor.ClientInterceptor{barrier}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: 5 * time.Second, Register: func(r worker.Registry) error {
		r.RegisterWorkflow(nativeGracefulResultWorkflow)
		r.RegisterActivityWithOptions(func(context.Context) (string, error) { return "successful-result", nil }, activity.RegisterOptions{Name: "graceful-result"})
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	sdk, _ := temporalqueue.Client(q)
	run, err := sdk.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeGracefulResultWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("inherited interceptor never entered")
	}
	if err := q.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement := newDriverResource(t, name, func(r worker.Registry) error {
		r.RegisterWorkflow(nativeGracefulResultWorkflow)
		r.RegisterActivityWithOptions(func(context.Context) (string, error) {
			t.Error("admitted callback was retried")
			return "unexpected", nil
		}, activity.RegisterOptions{Name: "graceful-result"})
		return nil
	})
	if err := replacement.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, _ := temporalqueue.Client(replacement)
	var result string
	if err := reader.GetWorkflow(t.Context(), run.GetID(), run.GetRunID()).Get(t.Context(), &result); err != nil || result != "successful-result" {
		t.Fatalf("admitted inherited callback lost completion: %q %v", result, err)
	}
}
