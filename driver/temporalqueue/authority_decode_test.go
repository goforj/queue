package temporalqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// authorityDecodeFixture separates temporary adapter availability from deterministic original-input rejection.
type authorityDecodeFixture struct {
	converter.DataConverter
	unavailable bool
}

// DecodingLimits declares the fixture's bounded adapter policy.
func (*authorityDecodeFixture) DecodingLimits() SerializationLimits {
	return DefaultSerializationLimits()
}

// MaxEncodedOverhead preserves the default converter reservation.
func (*authorityDecodeFixture) MaxEncodedOverhead() int { return 256 }

// FromPayloads can reject authoritative history independently of decoding the activity's own input.
func (f *authorityDecodeFixture) FromPayloads(input *common.Payloads, values ...any) error {
	if f.unavailable {
		return ErrAdapterUnavailable
	}
	return f.DataConverter.FromPayloads(input, values...)
}

// TestDeliveryAuthorityDecodePreservesFailureCategory checks original-history rejection at the real SDK activity boundary.
func TestDeliveryAuthorityDecodePreservesFailureCategory(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "unavailable"}[unavailable], func(t *testing.T) {
			d := delivery{Version: 1, Type: "business", WorkflowQueue: "reports", ActivityQueue: "reports", Metadata: busruntime.DeliveryMetadata{SchemaVersion: 1, DispatchID: "dispatch", JobID: "member", Queue: "reports"}}
			if err := d.resolve(time.Second); err != nil {
				t.Fatal(err)
			}
			input, err := converter.GetDefaultDataConverter().ToPayloads("wrong original input type")
			if err != nil {
				t.Fatal(err)
			}
			start := &history.WorkflowExecutionStartedEventAttributes{WorkflowType: &common.WorkflowType{Name: jobWorkflowType}, TaskQueue: &taskqueue.TaskQueue{Name: "reports"}, Input: input, Attempt: 1, OriginalExecutionRunId: "default-test-run-id", FirstExecutionRunId: "default-test-run-id"}
			service := &acceptanceHistory{response: &workflowservice.GetWorkflowExecutionHistoryResponse{History: &history.History{Events: []*history.HistoryEvent{{EventId: 1, Attributes: &history.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: start}}}}}}
			codec, err := newGuardedConverter(&authorityDecodeFixture{DataConverter: converter.GetDefaultDataConverter(), unavailable: unavailable}, DefaultSerializationLimits(), newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			b := &backend{gate: newAdmission(), taskQueue: "reports", workflowQueue: "reports", activityQueue: "reports", clientBinding: &ClientBinding{client: &acceptanceClient{service: service}, options: client.Options{Namespace: "default-test-namespace", DataConverter: codec}}, handlers: map[string]queue.Handler{"business": func(context.Context, queue.Job) error { calls++; return nil }}}
			env := guardedWorkflowEnvironment(t)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "dispatch", TaskQueue: "reports"})
			env.RegisterActivityWithOptions(b.executeDelivery, activity.RegisterOptions{Name: deliveryActivityType})
			env.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
				ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: "reports", ActivityID: "member", StartToCloseTimeout: time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
				return workflow.ExecuteActivity(ctx, deliveryActivityType, d).Get(ctx, nil)
			}, workflow.RegisterOptions{Name: jobWorkflowType})
			env.ExecuteWorkflow(jobWorkflowType)
			if unavailable {
				var category *temporal.ApplicationError
				if !errors.As(env.GetWorkflowError(), &category) || category.Type() != "goforj.unavailable.v1" || category.NonRetryable() {
					t.Fatal("authority outage lost retryability", env.GetWorkflowError())
				}
			} else {
				requirePropagationCategory(t, env.GetWorkflowError(), false)
			}
			if calls != 0 {
				t.Fatal("authority rejection reached business handler")
			}
		})
	}
}
