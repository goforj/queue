package temporalqueue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	engine "github.com/goforj/queue/internal/workflow"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
)

// businessSchedulingFixture isolates a bounded propagation failure to an immutable member identity.
type businessSchedulingFixture struct {
	rejectID string
	invalid  bool
}

// MaxDecodedBytes declares the ordinary fixture's decoding budget.
func (*businessSchedulingFixture) MaxDecodedBytes() int { return 1024 }

// Inject permits initial acceptance before scheduling preflight is exercised.
func (*businessSchedulingFixture) Inject(context.Context, workflow.HeaderWriter) error { return nil }

// Extract leaves already-scheduled activity context untouched.
func (*businessSchedulingFixture) Extract(ctx context.Context, _ workflow.HeaderReader) (context.Context, error) {
	return ctx, nil
}

// ExtractToWorkflow admits the accepted coordinator input.
func (*businessSchedulingFixture) ExtractToWorkflow(ctx workflow.Context, _ workflow.HeaderReader) (workflow.Context, error) {
	return ctx, nil
}

// InjectFromWorkflow rejects before the SDK creates a command, rather than returning an activity outcome.
func (f *businessSchedulingFixture) InjectFromWorkflow(ctx workflow.Context, _ workflow.HeaderWriter) error {
	if workflow.GetActivityOptions(ctx).ActivityID == f.rejectID {
		if f.invalid {
			return errors.New("synthetic scheduling context rejection")
		}
		return ErrAdapterUnavailable
	}
	return nil
}

// TestBusinessSchedulingPreflightDoesNotSettleUnscheduledMembers covers chain, batch, allowed failures, delay and previously admitted siblings.
func TestBusinessSchedulingPreflightDoesNotSettleUnscheduledMembers(t *testing.T) {
	for _, mode := range []string{"chain", "batch", "allowed batch", "delayed batch", "admitted sibling", "invalid", "control"} {
		t.Run(mode, func(t *testing.T) {
			kind, allow := "batch", mode == "allowed batch"
			if mode == "chain" || mode == "invalid" {
				kind = "chain"
			}
			jobs := []engine.Job{{Type: "business"}}
			if mode == "delayed batch" {
				jobs[0].Options.Delay = time.Second
			}
			if mode == "admitted sibling" || mode == "control" {
				jobs = []engine.Job{{Type: "first"}, {Type: "business"}, {Type: "later"}}
			}
			c, sdk := coordinatorAcceptanceFixture(t)
			if _, err := c.start(t.Context(), kind, "", "", allow, jobs, []engine.ContinuationDefinition{{Phase: "completion", Job: engine.StoredJob{Type: "hook"}}}); err != nil {
				t.Fatal(err)
			}
			def := sdk.inputs[0].(workflowDefinition)
			def.Resource = "default-test-namespace:reports"
			index := 0
			if mode == "admitted sibling" || mode == "control" {
				index = 1
			}
			fixture := &businessSchedulingFixture{rejectID: def.Jobs[index].Metadata.JobID, invalid: mode == "invalid"}
			if mode == "control" {
				fixture.rejectID = ""
			}
			env := guardedWorkflowEnvironment(t)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: def.ID, TaskQueue: "reports"})
			env.SetContextPropagators([]workflow.ContextPropagator{&guardedPropagator{inner: fixture, gate: newAdmission()}})
			env.RegisterWorkflowWithOptions(coordinatorWorkflowFixture, workflow.RegisterOptions{Name: coordinatorWorkflowType})
			var first, business, later, hooks atomic.Int32
			env.RegisterActivityWithOptions(func(_ context.Context, d delivery) error {
				switch d.Type {
				case "first":
					first.Add(1)
				case "business":
					business.Add(1)
				case "later":
					later.Add(1)
				case "hook":
					hooks.Add(1)
				}
				return nil
			}, activity.RegisterOptions{Name: deliveryActivityType})
			var deltas []journalDelta
			env.OnUpsertMemo(mock.Anything).Run(func(args mock.Arguments) {
				deltas = append(deltas, args.Get(0).(map[string]interface{})[journalKey].(journalValue).Delta)
			}).Return(nil)
			env.ExecuteWorkflow(coordinatorWorkflowType, def)
			if mode == "control" {
				if err := env.GetWorkflowError(); err != nil || first.Load() != 1 || business.Load() != 1 || later.Load() != 1 || hooks.Load() != 1 {
					t.Fatal("normal scheduling control failed", first.Load(), business.Load(), later.Load(), hooks.Load(), err)
				}
				return
			}
			requirePropagationCategory(t, env.GetWorkflowError(), mode != "invalid")
			if business.Load() != 0 || later.Load() != 0 || hooks.Load() != 0 {
				t.Fatal("unscheduled work reached application code", business.Load(), later.Load(), hooks.Load())
			}
			if mode == "admitted sibling" && first.Load() != 1 {
				t.Fatal("already admitted sibling did not drain")
			}
			for _, delta := range deltas {
				if delta.Index == index || delta.Failed || delta.Terminal || len(delta.Intents) > 0 {
					t.Fatal("preflight failure became a business settlement", delta)
				}
			}
		})
	}
}
