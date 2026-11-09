package temporalqueue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
	engine "github.com/goforj/queue/internal/workflow"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// memoContextFixture checks a non-yielding interceptor sees the actual publishing coroutine's activity policy.
type memoContextFixture struct {
	interceptor.WorkerInterceptorBase
	publications int
}

// InterceptWorkflow retains the fixture counter without replacing production orchestration.
func (f *memoContextFixture) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &memoContextInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, fixture: f}
}

// memoContextInbound installs a command-preserving outbound observer.
type memoContextInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	fixture *memoContextFixture
}

// Init leaves SDK registration and execution unchanged around the observer.
func (i *memoContextInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&memoContextOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}, fixture: i.fixture})
}

// memoContextOutbound verifies context identity without yielding or adding workflow commands.
type memoContextOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	fixture *memoContextFixture
}

// UpsertMemo rejects hook publication with another coroutine's activity context.
func (i *memoContextOutbound) UpsertMemo(ctx workflow.Context, memo map[string]interface{}) error {
	if value, ok := memo[journalKey].(journalValue); ok && value.Delta.InvocationID != "" {
		if workflow.GetActivityOptions(ctx).ActivityID != value.Delta.InvocationID {
			return invalidAdapterError()
		}
		i.fixture.publications++
	}
	return i.Next.UpsertMemo(ctx, memo)
}

// TestJournalUsesPublishingCoroutineContext keeps SDK and converter metadata attached to the actual ledger command.
func TestJournalUsesPublishingCoroutineContext(t *testing.T) {
	c, sdk := coordinatorAcceptanceFixture(t)
	if _, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business"}}, []engine.ContinuationDefinition{{Phase: "completion", Job: engine.StoredJob{Type: "hook"}}}); err != nil {
		t.Fatal(err)
	}
	def := sdk.inputs[0].(workflowDefinition)
	def.Resource = "default-test-namespace:reports"
	fixture := &memoContextFixture{}
	env := guardedWorkflowEnvironment(t)
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{fixture}})
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: def.ID, TaskQueue: "reports"})
	env.RegisterWorkflowWithOptions(coordinatorWorkflowFixture, workflow.RegisterOptions{Name: coordinatorWorkflowType})
	env.RegisterActivityWithOptions(func(context.Context, delivery) error { return nil }, activity.RegisterOptions{Name: deliveryActivityType})
	env.ExecuteWorkflow(coordinatorWorkflowType, def)
	if err := env.GetWorkflowError(); err != nil || fixture.publications != 2 {
		t.Fatal("journal lost publishing context", fixture.publications, err)
	}
}

// TestDefinitionChecksPrecedeJournalReservation rejects invalid counts and identities before constructing a per-hook reservation.
func TestDefinitionChecksPrecedeJournalReservation(t *testing.T) {
	c, sdk := coordinatorAcceptanceFixture(t)
	if _, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business"}}, []engine.ContinuationDefinition{{Phase: "completion", Job: engine.StoredJob{Type: "hook"}}}); err != nil {
		t.Fatal(err)
	}
	base := sdk.inputs[0].(workflowDefinition)
	if err := base.validate(); err != nil {
		t.Fatal("valid control rejected", err)
	}
	for _, mode := range []string{"hooks", "members", "dispatch identity"} {
		t.Run(mode, func(t *testing.T) {
			def := base
			def.JournalBudget = 1024
			want := "invalid Temporal workflow definition"
			switch mode {
			case "hooks":
				def.Hooks = make([]hookDefinition, 33)
				want = "too many workflow hooks"
			case "members":
				def.Jobs = make([]delivery, maxMembers+1)
				def.Hooks = make([]hookDefinition, 32)
			case "dispatch identity":
				def.DispatchID = strings.Repeat("d", 2048)
				want = "invalid Temporal workflow member identity"
			}
			if err := def.validate(); err == nil || err.Error() != want {
				t.Fatal("reservation ran before input validation", err, want)
			}
		})
	}
}

// selectedJournalFailure injects failure at a committed transition boundary without changing input conversion.
type selectedJournalFailure struct {
	converter.DataConverter
	sequence int
}

// DecodingLimits retains the bounded adapter contract while exercising production workflow publication.
func (*selectedJournalFailure) DecodingLimits() SerializationLimits {
	return DefaultSerializationLimits()
}

// MaxEncodedOverhead matches the authoritative default converter's journal reservation.
func (*selectedJournalFailure) MaxEncodedOverhead() int { return 256 }

// ToPayload rejects only the selected transition, before SDK fallback can publish an unprotected delta.
func (c *selectedJournalFailure) ToPayload(value any) (*common.Payload, error) {
	if delta, ok := value.(journalDelta); ok && delta.Sequence == c.sequence {
		return nil, errors.New("private journal adapter diagnostic")
	}
	return c.DataConverter.ToPayload(value)
}

// coordinatorWorkflowFixture supplies lineage omitted by SDK test environments while using the actual production coordinator.
func coordinatorWorkflowFixture(ctx workflow.Context, def workflowDefinition) error {
	info := workflow.GetInfo(ctx)
	info.OriginalRunID = info.WorkflowExecution.RunID
	info.FirstRunID = info.OriginalRunID
	return coordinatorWorkflow(ctx, def)
}

// TestJournalFailureStopsAtBusinessScheduleAndSettlementBoundaries verifies adapter loss never repeats a business or follow-up effect.
func TestJournalFailureStopsAtBusinessScheduleAndSettlementBoundaries(t *testing.T) {
	for _, sequence := range []int{0, 1, 2, 3} {
		t.Run(map[int]string{0: "control", 1: "business commit", 2: "hook scheduling", 3: "hook settlement"}[sequence], func(t *testing.T) {
			c, sdk := coordinatorAcceptanceFixture(t)
			if _, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business"}}, []engine.ContinuationDefinition{{Phase: "completion", Job: engine.StoredJob{Type: "hook"}}}); err != nil {
				t.Fatal(err)
			}
			def := sdk.inputs[0].(workflowDefinition)
			def.Resource = "default-test-namespace:reports"
			env := guardedWorkflowEnvironment(t)
			codecFailureSequence := sequence
			if sequence > 1 {
				codecFailureSequence = 0
				env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&memoOperationFailure{sequence: sequence}}})
			}
			dc, err := newGuardedConverter(&selectedJournalFailure{DataConverter: converter.GetDefaultDataConverter(), sequence: codecFailureSequence}, DefaultSerializationLimits(), newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			fc, err := newGuardedFailureConverter(nil, dc, newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			env.SetDataConverter(dc)
			env.SetFailureConverter(fc)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: def.ID, TaskQueue: "reports"})
			env.RegisterWorkflowWithOptions(coordinatorWorkflowFixture, workflow.RegisterOptions{Name: coordinatorWorkflowType})
			business, hooks := 0, 0
			env.RegisterActivityWithOptions(func(_ context.Context, d delivery) error {
				if d.Type == "business" {
					business++
				} else {
					hooks++
				}
				return nil
			}, activity.RegisterOptions{Name: deliveryActivityType})
			// Retained histories use Memo-first ordering; server tests cover the new atomic command-first path.
			env.OnGetVersion("goforj.queue.continuation.schedule-command", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
			env.ExecuteWorkflow(coordinatorWorkflowType, def)
			if sequence == 0 {
				if err := env.GetWorkflowError(); err != nil {
					t.Fatal(err)
				}
			} else {
				requirePropagationCategory(t, env.GetWorkflowError(), false)
			}
			wantHooks := 1
			if sequence == 1 || sequence == 2 {
				wantHooks = 0
			}
			if business != 1 || hooks != wantHooks {
				t.Fatalf("effects repeated or scheduled across failed journal: business=%d hooks=%d wantHooks=%d", business, hooks, wantHooks)
			}
		})
	}
}

// TestCoordinatorEntryRejectsResourceAndDefinitionBeforeBusiness checks the workflow-side acceptance boundary independently of producer validation.
func TestCoordinatorEntryRejectsResourceAndDefinitionBeforeBusiness(t *testing.T) {
	for _, mode := range []string{"resource", "version"} {
		t.Run(mode, func(t *testing.T) {
			c, sdk := coordinatorAcceptanceFixture(t)
			if _, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business"}}, nil); err != nil {
				t.Fatal(err)
			}
			def := sdk.inputs[0].(workflowDefinition)
			def.Resource = "default-test-namespace:reports"
			if mode == "resource" {
				def.Resource = "different:reports"
			} else {
				def.Version = 99
			}
			env := guardedWorkflowEnvironment(t)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: def.ID, TaskQueue: "reports"})
			env.RegisterWorkflowWithOptions(coordinatorWorkflowFixture, workflow.RegisterOptions{Name: coordinatorWorkflowType})
			calls := 0
			env.RegisterActivityWithOptions(func(context.Context, delivery) error { calls++; return nil }, activity.RegisterOptions{Name: deliveryActivityType})
			env.ExecuteWorkflow(coordinatorWorkflowType, def)
			requirePropagationCategory(t, env.GetWorkflowError(), false)
			if calls != 0 {
				t.Fatal("invalid coordinator entered business activity")
			}
		})
	}
}

// TestCanceledHookRetainsIndependentOutcomeAndReleasesLaterHook prevents a hook interruption from changing business completion.
func TestCanceledHookRetainsIndependentOutcomeAndReleasesLaterHook(t *testing.T) {
	c, sdk := coordinatorAcceptanceFixture(t)
	if _, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business"}}, []engine.ContinuationDefinition{{Phase: "success", Job: engine.StoredJob{Type: "cancelled", Options: engine.JobOptions{Delay: time.Second}}}, {Phase: "completion", Job: engine.StoredJob{Type: "later"}}}); err != nil {
		t.Fatal(err)
	}
	def := sdk.inputs[0].(workflowDefinition)
	def.Resource = "default-test-namespace:reports"
	env := guardedWorkflowEnvironment(t)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: def.ID, TaskQueue: "reports"})
	env.RegisterWorkflowWithOptions(coordinatorWorkflowFixture, workflow.RegisterOptions{Name: coordinatorWorkflowType})
	var deltas []journalDelta
	env.OnUpsertMemo(mock.Anything).Run(func(args mock.Arguments) {
		deltas = append(deltas, args.Get(0).(map[string]interface{})[journalKey].(journalValue).Delta)
	}).Return(nil)
	later := 0
	env.RegisterActivityWithOptions(func(_ context.Context, d delivery) error {
		if d.Type == "cancelled" {
			return temporal.NewCanceledError("independent hook cancellation")
		}
		if d.Type == "later" {
			later++
		}
		return nil
	}, activity.RegisterOptions{Name: deliveryActivityType})
	env.ExecuteWorkflow(coordinatorWorkflowType, def)
	if err := env.GetWorkflowError(); err != nil || later != 1 {
		t.Fatal("hook interruption changed business outcome or stranded successor", later, err)
	}
	var invocations []string
	settled := make(map[string]string)
	for _, delta := range deltas {
		for _, intent := range delta.Intents {
			invocations = append(invocations, intent.Outcome.Invocation.ID)
		}
		if delta.InvocationID != "" && delta.Status != "scheduled" {
			settled[delta.InvocationID] = delta.Status
		}
	}
	if len(invocations) != 2 || settled[invocations[0]] != "interrupted" || settled[invocations[1]] != "succeeded" {
		t.Fatalf("incorrect durable hook outcomes: invocations=%v statuses=%v", invocations, settled)
	}

}

// memoOperationFailure injects SDK operation errors before the test environment's nil-memo merge on a later encoding failure.
// First-update conversion rejection is exercised through the actual guarded converter separately.
type memoOperationFailure struct {
	interceptor.WorkerInterceptorBase
	sequence int
}

// InterceptWorkflow keeps application orchestration intact while selecting one SDK publication boundary.
func (i *memoOperationFailure) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &memoFailureInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, sequence: i.sequence}
}

// memoFailureInbound installs a controlled SDK outbound error without replacing the production coordinator.
type memoFailureInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	sequence int
}

// Init preserves the SDK outbound chain for every successful operation.
func (i *memoFailureInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&memoFailureOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}, sequence: i.sequence})
}

// memoFailureOutbound models the operation's declared error result at one transition.
type memoFailureOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	sequence int
}

// UpsertMemo prevents a test-harness panic from obscuring the coordinator's response to a failed SDK command.
func (i *memoFailureOutbound) UpsertMemo(ctx workflow.Context, memo map[string]interface{}) error {
	if memo[journalKey].(journalValue).Delta.Sequence == i.sequence {
		return invalidAdapterError()
	}
	return i.Next.UpsertMemo(ctx, memo)
}

// TestTemporalChainFailureHookCountersIncludeCommittedFailure keeps portable summaries aligned with built-in stores while preserving the successful-node cursor.
func TestTemporalChainFailureHookCountersIncludeCommittedFailure(t *testing.T) {
	for _, mode := range []string{"first", "later"} {
		t.Run(mode, func(t *testing.T) {
			c, sdk := coordinatorAcceptanceFixture(t)
			jobs := []engine.Job{{Type: "first"}, {Type: "later"}}
			hooks := []engine.ContinuationDefinition{{Phase: "failure", Job: engine.StoredJob{Type: "failure-hook"}}, {Phase: "completion", Job: engine.StoredJob{Type: "completion-hook"}}}
			if _, err := c.start(t.Context(), "chain", "", "", false, jobs, hooks); err != nil {
				t.Fatal(err)
			}
			def := sdk.inputs[0].(workflowDefinition)
			def.Resource = "default-test-namespace:reports"
			env := guardedWorkflowEnvironment(t)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: def.ID, TaskQueue: "reports"})
			env.RegisterWorkflowWithOptions(coordinatorWorkflowFixture, workflow.RegisterOptions{Name: coordinatorWorkflowType})
			var summaries []busruntime.ContinuationSummary
			env.OnUpsertMemo(mock.Anything).Run(func(args mock.Arguments) {
				delta := args.Get(0).(map[string]interface{})[journalKey].(journalValue).Delta
				for _, intent := range delta.Intents {
					summaries = append(summaries, intent.Outcome.Invocation.Trigger)
				}
			}).Return(nil)
			env.RegisterActivityWithOptions(func(_ context.Context, d delivery) error {
				if d.Type == mode {
					return temporal.NewNonRetryableApplicationError("business failed", "fixture", nil)
				}
				return nil
			}, activity.RegisterOptions{Name: deliveryActivityType})
			env.ExecuteWorkflow(coordinatorWorkflowType, def)
			var failure *temporal.ApplicationError
			if !errors.As(env.GetWorkflowError(), &failure) || failure.Type() != "goforj.business-failed.v1" {
				t.Fatal("chain did not fail", env.GetWorkflowError())
			}
			if len(summaries) != 2 {
				t.Fatal("failure/completion definitions not triggered", summaries)
			}
			processed, next := 1, 0
			if mode == "later" {
				processed, next = 2, 1
			}
			for _, summary := range summaries {
				if summary.Processed != processed || summary.Pending != 2-processed || summary.NextIndex != next || summary.Failed != 1 || !summary.Cancelled || summary.Completed {
					t.Fatalf("incorrect portable failed-chain counters: %+v", summary)
				}
			}
		})
	}
}
