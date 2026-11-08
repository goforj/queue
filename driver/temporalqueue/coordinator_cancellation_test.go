package temporalqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// TestIndependentBusinessCancellationFailsAfterDrain prevents successful SDK completion with pending business members.
func TestIndependentBusinessCancellationFailsAfterDrain(t *testing.T) {
	for _, kind := range []string{"chain", "batch", "batch-allow-failures"} {
		t.Run(kind, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			env.SetDataConverter(dc)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "work", TaskQueue: "tasks"})
			workflowKind := kind
			if kind == "batch-allow-failures" {
				workflowKind = "batch"
			}
			member := delivery{Version: 1, Type: "business", WorkflowQueue: "tasks", ActivityQueue: "tasks", Metadata: busruntime.DeliveryMetadata{SchemaVersion: 1, DispatchID: "dispatch", JobID: "member", Queue: "tasks"}}
			if workflowKind == "chain" {
				member.Metadata.ChainID = "work"
			} else {
				member.Metadata.BatchID = "work"
			}
			if err := member.resolve(time.Second); err != nil {
				t.Fatal(err)
			}
			sibling := member
			sibling.Metadata.JobID = "sibling"
			sibling.Delay = time.Hour
			if err := sibling.resolve(time.Second); err != nil {
				t.Fatal(err)
			}
			def := workflowDefinition{JournalBudget: 256 << 10, EncodingOverhead: 256, Version: 1, Kind: workflowKind, AllowFailures: kind == "batch-allow-failures", ID: "work", DispatchID: "dispatch", Queue: "tasks", WorkflowQueue: "tasks", Resource: "default-test-namespace:tasks", CreatedAt: time.Now().UTC(), Jobs: []delivery{member, sibling}}
			parentCanceled := false
			env.RegisterWorkflowWithOptions(func(ctx workflow.Context, def workflowDefinition) error {
				info := workflow.GetInfo(ctx)
				info.OriginalRunID = info.WorkflowExecution.RunID
				info.FirstRunID = info.OriginalRunID
				err := coordinatorWorkflow(ctx, def)
				parentCanceled = ctx.Err() != nil
				return err
			}, workflow.RegisterOptions{Name: coordinatorWorkflowType})
			calls := 0
			env.RegisterActivityWithOptions(func(_ context.Context, d delivery) error {
				calls++
				if d.Metadata.JobID == "member" {
					return temporal.NewCanceledError("independent activity cancellation")
				}
				return nil
			}, activity.RegisterOptions{Name: deliveryActivityType})
			var deltas []journalDelta
			env.OnUpsertMemo(mock.Anything).Run(func(args mock.Arguments) {
				deltas = append(deltas, args.Get(0).(map[string]interface{})[journalKey].(journalValue).Delta)
			}).Return(nil)
			env.ExecuteWorkflow(coordinatorWorkflowType, def)
			var application *temporal.ApplicationError
			if parentCanceled || !errors.As(env.GetWorkflowError(), &application) || application.Type() != "goforj.workflow-interrupted.v1" || !application.NonRetryable() {
				t.Fatalf("independent cancellation classification: parent=%t error=%v", parentCanceled, env.GetWorkflowError())
			}
			want := 1
			if workflowKind == "batch" {
				want = 2
			}
			if calls != want || len(deltas) != want || !deltas[0].Skipped || deltas[0].Failed || deltas[0].Terminal {
				t.Fatalf("incorrect interruption or drain: calls=%d deltas=%+v", calls, deltas)
			}
			if want == 2 && (deltas[1].Index != 1 || deltas[1].Skipped || deltas[1].Failed || deltas[1].Terminal) {
				t.Fatalf("admitted sibling did not settle independently: %+v", deltas[1])
			}
			if err := validateJournal(def, deltas, "", nil); err != nil {
				t.Fatalf("interrupted journal rejected: %v", err)
			}
		})
	}
}
