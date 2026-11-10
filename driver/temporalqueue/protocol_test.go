package temporalqueue

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// TestPortableWorkflowRejectsDirectStartPolicy prevents retry/cron and identity bypasses before user entry.
func TestPortableWorkflowRejectsDirectStartPolicy(t *testing.T) {
	for name, mutate := range map[string]func(*client.StartWorkflowOptions, *delivery){
		"workflow identity":  func(_ *client.StartWorkflowOptions, d *delivery) { d.Metadata.DispatchID = "another-workflow" },
		"task queue":         func(_ *client.StartWorkflowOptions, d *delivery) { d.WorkflowQueue = "another-task-queue" },
		"schema":             func(_ *client.StartWorkflowOptions, d *delivery) { d.Version = 99 },
		"reserved job":       func(_ *client.StartWorkflowOptions, d *delivery) { d.Type = deliveryActivityType },
		"unbounded retry":    func(_ *client.StartWorkflowOptions, d *delivery) { d.Retry = math.MaxInt32 },
		"unresolved timeout": func(_ *client.StartWorkflowOptions, d *delivery) { d.Resolved.Timeout = 2 * time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			options := client.StartWorkflowOptions{ID: "dispatch", TaskQueue: "task-queue"}
			d := delivery{WorkflowQueue: "task-queue", ActivityQueue: "task-queue", Version: 1, Type: "business", Timeout: time.Second, Metadata: busruntime.DeliveryMetadata{SchemaVersion: 1, DispatchID: "dispatch", JobID: "job", Queue: "task-queue"}}
			if err := d.resolve(time.Second); err != nil {
				t.Fatal(err)
			}
			mutate(&options, &d)
			env.SetStartWorkflowOptions(options)
			env.RegisterWorkflowWithOptions(jobWorkflow, workflow.RegisterOptions{Name: jobWorkflowType})
			env.ExecuteWorkflow(jobWorkflowType, d)
			err := env.GetWorkflowError()
			var application *temporal.ApplicationError
			if !errors.As(err, &application) || !application.NonRetryable() || application.Type() != "goforj.invalid.v1" {
				t.Fatalf("unsafe direct invocation result %v", err)
			}
		})
	}
}

// TestPortableExecutionPolicy validates server fields that the SDK test environment does not reproduce.
func TestPortableExecutionPolicy(t *testing.T) {
	valid := workflow.Info{WorkflowExecution: workflow.Execution{ID: "dispatch", RunID: "run"}, WorkflowType: workflow.Type{Name: jobWorkflowType}, TaskQueueName: "task-queue", Attempt: 1, FirstRunID: "run", OriginalRunID: "run"}
	if err := validatePortableInfo(&valid, "dispatch", "task-queue", jobWorkflowType); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*workflow.Info){
		"retry":     func(info *workflow.Info) { info.RetryPolicy = &temporal.RetryPolicy{MaximumAttempts: 2} },
		"cron":      func(info *workflow.Info) { info.CronSchedule = "@hourly" },
		"attempt":   func(info *workflow.Info) { info.Attempt = 2 },
		"continued": func(info *workflow.Info) { info.ContinuedExecutionRunID = "previous" },
		"lineage":   func(info *workflow.Info) { info.FirstRunID = "other" },
		"child":     func(info *workflow.Info) { info.ParentWorkflowExecution = &workflow.Execution{ID: "parent"} },
	} {
		t.Run(name, func(t *testing.T) {
			info := valid
			mutate(&info)
			if err := validatePortableInfo(&info, "dispatch", "task-queue", jobWorkflowType); err == nil {
				t.Fatal("unsupported execution policy accepted")
			}
		})
	}
}
