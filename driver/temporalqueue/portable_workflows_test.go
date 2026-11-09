package temporalqueue

import (
	"context"
	"fmt"
	"testing"

	engine "github.com/goforj/queue/internal/workflow"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TestPortableJobResourceRouting checks combined and split routes before the first activity is admitted.
func TestPortableJobResourceRouting(t *testing.T) {
	for _, split := range []bool{false, true} {
		for _, mode := range []string{"valid", "logical queue", "workflow queue", "activity queue", "namespace", "legacy"} {
			t.Run(fmt.Sprintf("split=%t/%s", split, mode), func(t *testing.T) {
				c, sdk := coordinatorAcceptanceFixture(t)
				if _, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business"}}, nil); err != nil {
					t.Fatal(err)
				}
				base := sdk.inputs[0].(workflowDefinition)
				policy := portableWorkflows{namespace: "default-test-namespace", queue: base.Queue, workflowQueue: base.WorkflowQueue, activityQueue: base.Jobs[0].ActivityQueue}
				if split {
					policy.workflowQueue = "reports-workflows"
					policy.activityQueue = "reports-activities"
				}
				input := base.Jobs[0]
				input.Metadata.ChainID = ""
				input.WorkflowQueue = policy.workflowQueue
				input.ActivityQueue = policy.activityQueue
				switch mode {
				case "logical queue":
					input.Metadata.Queue = "other"
				case "workflow queue":
					input.WorkflowQueue = "other"
				case "activity queue", "legacy":
					input.ActivityQueue = "other"
				case "namespace":
					policy.namespace = "other"
				}
				env := guardedWorkflowEnvironment(t)
				env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: input.Metadata.DispatchID, TaskQueue: policy.workflowQueue})
				env.RegisterWorkflowWithOptions(func(ctx workflow.Context, d delivery) error {
					info := workflow.GetInfo(ctx)
					info.OriginalRunID = info.WorkflowExecution.RunID
					info.FirstRunID = info.OriginalRunID
					return policy.jobWorkflow(ctx, d)
				}, workflow.RegisterOptions{Name: jobWorkflowType})
				calls := 0
				env.RegisterActivityWithOptions(func(ctx context.Context, _ delivery) error {
					calls++
					if activity.GetInfo(ctx).TaskQueue != input.ActivityQueue {
						t.Fatal("scheduled activity route changed")
					}
					return nil
				}, activity.RegisterOptions{Name: deliveryActivityType})
				if mode == "legacy" {
					env.OnGetVersion(portableRoutingChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
				}
				env.ExecuteWorkflow(jobWorkflowType, input)
				if mode == "valid" || mode == "legacy" {
					if err := env.GetWorkflowError(); err != nil || calls != 1 {
						t.Fatal("supported route rejected", calls, err)
					}
				} else {
					requirePropagationCategory(t, env.GetWorkflowError(), false)
					if calls != 0 {
						t.Fatal("route rejection followed activity execution")
					}
				}
			})
		}
	}
}

// TestPortableCoordinatorResourceRouting checks all member/hook targets before journal publication and application commands.
func TestPortableCoordinatorResourceRouting(t *testing.T) {
	for _, kind := range []string{"chain", "batch"} {
		for _, mode := range []string{"valid", "split", "member activity", "hook activity", "member workflow", "hook workflow", "logical queue", "definition workflow", "resource", "members", "hooks"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				c, sdk := coordinatorAcceptanceFixture(t)
				if _, err := c.start(t.Context(), kind, "", "", false, []engine.Job{{Type: "business"}}, []engine.ContinuationDefinition{{Phase: "completion", Job: engine.StoredJob{Type: "hook"}}}); err != nil {
					t.Fatal(err)
				}
				input := sdk.inputs[0].(workflowDefinition)
				policy := portableWorkflows{namespace: "default-test-namespace", queue: input.Queue, workflowQueue: input.WorkflowQueue, activityQueue: input.Jobs[0].ActivityQueue}
				if mode == "split" {
					policy.workflowQueue = "reports-workflows"
					policy.activityQueue = "reports-activities"
					input.WorkflowQueue = policy.workflowQueue
					input.Jobs[0].WorkflowQueue = policy.workflowQueue
					input.Jobs[0].ActivityQueue = policy.activityQueue
					input.Hooks[0].Job.WorkflowQueue = policy.workflowQueue
					input.Hooks[0].Job.ActivityQueue = policy.activityQueue
				}
				input.Resource = policy.namespace + ":" + policy.queue
				switch mode {
				case "member activity":
					input.Jobs[0].ActivityQueue = "other"
				case "hook activity":
					input.Hooks[0].Job.ActivityQueue = "other"
				case "member workflow":
					input.Jobs[0].WorkflowQueue = "other"
				case "hook workflow":
					input.Hooks[0].Job.WorkflowQueue = "other"
				case "logical queue":
					input.Queue = "other"
				case "definition workflow":
					input.WorkflowQueue = "other"
				case "resource":
					input.Resource = "other"
				case "members":
					input.Jobs = make([]delivery, maxMembers+1)
				case "hooks":
					input.Hooks = make([]hookDefinition, 33)
				}
				env := guardedWorkflowEnvironment(t)
				capture := &semanticCapture{}
				env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{capture}})
				env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: input.ID, TaskQueue: policy.workflowQueue})
				env.RegisterWorkflowWithOptions(func(ctx workflow.Context, d workflowDefinition) error {
					info := workflow.GetInfo(ctx)
					info.OriginalRunID = info.WorkflowExecution.RunID
					info.FirstRunID = info.OriginalRunID
					return policy.coordinatorWorkflow(ctx, d)
				}, workflow.RegisterOptions{Name: coordinatorWorkflowType})
				calls := 0
				env.RegisterActivityWithOptions(func(context.Context, delivery) error { calls++; return nil }, activity.RegisterOptions{Name: deliveryActivityType})
				env.ExecuteWorkflow(coordinatorWorkflowType, input)
				if mode == "valid" || mode == "split" {
					if err := env.GetWorkflowError(); err != nil || calls != 2 || len(capture.deltas) == 0 {
						t.Fatal("supported coordinator route rejected", calls, err)
					}
				} else {
					requirePropagationCategory(t, env.GetWorkflowError(), false)
					if calls != 0 || len(capture.deltas) != 0 {
						t.Fatal("route rejection followed coordinator side effects", calls, len(capture.deltas))
					}
				}
			})
		}
	}
}
