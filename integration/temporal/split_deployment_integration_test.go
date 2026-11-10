//go:build integration

package temporal

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// nativePinnedSplitDeployment keeps workflow and activity execution on its pinned deployment version.
func nativePinnedSplitDeployment(ctx workflow.Context, activityQueue string) ([]string, error) {
	return nativeSplitDeploymentLoop(ctx, activityQueue)
}

// nativeAutoSplitDeployment follows deployment changes while retaining replay-compatible application code.
func nativeAutoSplitDeployment(ctx workflow.Context, activityQueue string) ([]string, error) {
	return nativeSplitDeploymentLoop(ctx, activityQueue)
}

// nativeSplitDeploymentLoop records actual activity-worker build results at each deployment stage.
func nativeSplitDeploymentLoop(ctx workflow.Context, activityQueue string) ([]string, error) {
	builds := []string{}
	if err := workflow.SetQueryHandler(ctx, "builds", func() ([]string, error) { return builds, nil }); err != nil {
		return nil, err
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: activityQueue, StartToCloseTimeout: 5 * time.Second, DisableEagerExecution: true})
	for {
		execute, done := false, false
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(workflow.GetSignalChannel(ctx, "step"), func(channel workflow.ReceiveChannel, _ bool) { channel.Receive(ctx, &done); execute = !done })
		selector.AddReceive(workflow.GetSignalChannel(ctx, "nudge"), func(channel workflow.ReceiveChannel, _ bool) { var ignored bool; channel.Receive(ctx, &ignored) })
		selector.Select(ctx)
		if done {
			return builds, nil
		}
		if execute {
			var build string
			if err := workflow.ExecuteActivity(ctx, "native-deployment-build").Get(ctx, &build); err != nil {
				return builds, err
			}
			builds = append(builds, build)
		}
	}
}

// waitNativeDeploymentVersion waits for observed workflow routing, creating only ordinary no-op signal tasks.
func waitNativeDeploymentVersion(t *testing.T, ctx context.Context, sdk client.Client, run client.WorkflowRun, build string, behavior enums.VersioningBehavior) {
	t.Helper()
	observed, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		description, err := sdk.DescribeWorkflowExecution(observed, run.GetID(), run.GetRunID())
		if err == nil {
			info := description.WorkflowExecutionInfo.GetVersioningInfo()
			if info.GetBehavior() == behavior && info.GetDeploymentVersion().GetBuildId() == build {
				return
			}
		}
		if err := sdk.SignalWorkflow(observed, run.GetID(), run.GetRunID(), "nudge", true); err != nil {
			t.Fatal(err)
		}
		select {
		case <-tick.C:
		case <-observed.Done():
			t.Fatal("deployment routing did not converge", build, observed.Err())
		}
	}
}

// TestTemporalDriverSplitDeploymentRolloutAndRollback verifies actual workflow and activity routing across both worker roles.
func TestTemporalDriverSplitDeploymentRolloutAndRollback(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("native-split-deployment-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	newVersion := func(build string) *queue.Queue {
		options := worker.Options{DeploymentOptions: worker.DeploymentOptions{UseVersioning: true, Version: worker.WorkerDeploymentVersion{DeploymentName: name, BuildID: build}, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}}
		q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
			DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress},
			WorkflowTaskQueue: name + "-workflows", ActivityTaskQueue: name + "-activities", WorkflowWorkerOptions: options, ActivityWorkerOptions: options, DefaultActivityTimeout: 5 * time.Second, ShutdownTimeout: 5 * time.Second,
			Register: func(r worker.Registry) error {
				r.RegisterWorkflowWithOptions(nativePinnedSplitDeployment, workflow.RegisterOptions{VersioningBehavior: workflow.VersioningBehaviorPinned})
				r.RegisterWorkflowWithOptions(nativeAutoSplitDeployment, workflow.RegisterOptions{VersioningBehavior: workflow.VersioningBehaviorAutoUpgrade})
				r.RegisterActivityWithOptions(func(context.Context) (string, error) { return build, nil }, activity.RegisterOptions{Name: "native-deployment-build"})
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := q.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
		if err := q.StartWorkers(ctx); err != nil {
			t.Fatal(err)
		}
		return q
	}
	first := newVersion("a")
	sdk, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	deployment := sdk.WorkerDeploymentClient().GetHandle(name)
	setCurrent := func(build string) {
		waitDriverState(t, func() bool {
			_, err := deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: build})
			return err == nil
		})
	}
	setCurrent("a")
	pinned, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name + "-pinned", TaskQueue: name + "-workflows"}, nativePinnedSplitDeployment, name+"-activities")
	if err != nil {
		t.Fatal(err)
	}
	auto, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name + "-auto", TaskQueue: name + "-workflows"}, nativeAutoSplitDeployment, name+"-activities")
	if err != nil {
		t.Fatal(err)
	}
	runs := []client.WorkflowRun{pinned, auto}
	step := func(index int, autoBuild string) {
		waitNativeDeploymentVersion(t, ctx, sdk, pinned, "a", enums.VERSIONING_BEHAVIOR_PINNED)
		waitNativeDeploymentVersion(t, ctx, sdk, auto, autoBuild, enums.VERSIONING_BEHAVIOR_AUTO_UPGRADE)
		for i, run := range runs {
			if err := sdk.SignalWorkflow(ctx, run.GetID(), run.GetRunID(), "step", false); err != nil {
				t.Fatal(err)
			}
			var builds []string
			waitDriverState(t, func() bool {
				query, err := sdk.QueryWorkflow(ctx, run.GetID(), run.GetRunID(), "builds")
				return err == nil && query.Get(&builds) == nil && len(builds) > index
			})
			want := "a"
			if i == 1 {
				want = autoBuild
			}
			if len(builds) != index+1 || builds[index] != want {
				t.Fatal("split activity-worker deployment mismatch", builds, want)
			}
		}
	}
	step(0, "a")
	second := newVersion("b")
	setCurrent("b")
	step(1, "b")
	setCurrent("a")
	waitNativeDeploymentVersion(t, ctx, sdk, auto, "a", enums.VERSIONING_BEHAVIOR_AUTO_UPGRADE)
	if err := second.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	step(2, "a")
	for i, run := range runs {
		if err := sdk.SignalWorkflow(ctx, run.GetID(), run.GetRunID(), "step", true); err != nil {
			t.Fatal(err)
		}
		var builds []string
		if err := run.Get(ctx, &builds); err != nil {
			t.Fatal(err)
		}
		want := "a,a,a"
		if i == 1 {
			want = "a,b,a"
		}
		if strings.Join(builds, ",") != want {
			t.Fatal("deployment history changed", builds, want)
		}
	}
}
