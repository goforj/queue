package temporalqueue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	engine "github.com/goforj/queue/internal/workflow"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TestGenericDeliveryValidatesOriginalAuthorityBeforeHandler checks actual SDK activity context against independently supplied original history.
func TestGenericDeliveryValidatesOriginalAuthorityBeforeHandler(t *testing.T) {
	for _, kind := range []string{"ordinary", "chain"} {
		t.Run(kind, func(t *testing.T) {
			modes := []string{"valid", "history outage", "reset lineage", "foreign workflow", "payload mismatch", "activity identity", "bad delivery", "worker route", "parent identity", "closed adapters", "handler panic"}
			if kind == "chain" {
				modes = append(modes, "resource", "definition identity", "invalid definition")
			}
			for _, mode := range modes {
				t.Run(mode, func(t *testing.T) {
					d := delivery{Version: 1, Type: "business", WorkflowQueue: "reports", ActivityQueue: "reports", Metadata: busruntime.DeliveryMetadata{SchemaVersion: 1, DispatchID: "dispatch", JobID: "member", Queue: "reports"}, Payload: []byte("exact bytes")}
					if err := d.resolve(time.Second); err != nil {
						t.Fatal(err)
					}
					var original any = d
					parent, workflowType := "dispatch", jobWorkflowType
					if kind == "chain" {
						c, sdk := coordinatorAcceptanceFixture(t)
						if _, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business", Payload: []byte("exact bytes")}}, nil); err != nil {
							t.Fatal(err)
						}
						def := sdk.inputs[0].(workflowDefinition)
						def.Resource = "default-test-namespace:reports"
						parent = def.ID
						workflowType = coordinatorWorkflowType
						d = def.Jobs[0]
						switch mode {
						case "resource":
							def.Resource = "different:reports"
						case "definition identity":
							def.ID = "different"
						case "invalid definition":
							def.Jobs[0].Resolved.Timeout = 0
						}
						original = def
					}
					payload, err := converter.GetDefaultDataConverter().ToPayloads(original)
					if err != nil {
						t.Fatal(err)
					}
					start := &history.WorkflowExecutionStartedEventAttributes{WorkflowType: &common.WorkflowType{Name: workflowType}, TaskQueue: &taskqueue.TaskQueue{Name: "reports"}, Input: payload, Attempt: 1, OriginalExecutionRunId: "default-test-run-id", FirstExecutionRunId: "default-test-run-id"}
					switch mode {
					case "reset lineage":
						start.OriginalExecutionRunId = "different-run"
					case "foreign workflow":
						start.WorkflowType.Name = "unrelated"
					case "payload mismatch":
						d.Payload = []byte("copied different payload")
					case "activity identity":
						d.Metadata.JobID = "different-member"
					case "bad delivery":
						d.Version = 99
					case "worker route":
						d.Metadata.Queue = "different"
					case "parent identity":
						if kind == "chain" {
							d.Metadata.ChainID = "different"
						} else {
							d.Metadata.DispatchID = "different"
						}
					}
					service := &acceptanceHistory{response: &workflowservice.GetWorkflowExecutionHistoryResponse{History: &history.History{Events: []*history.HistoryEvent{{EventId: 1, Attributes: &history.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: start}}}}}}
					if mode == "history outage" {
						service.err = serviceerror.NewUnavailable("private history diagnostic")
					}
					dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
					if err != nil {
						t.Fatal(err)
					}
					calls := 0
					gate := newAdmission()
					if mode == "closed adapters" {
						gate.close()
					}
					b := &backend{gate: gate, taskQueue: "reports", workflowQueue: "reports", activityQueue: "reports", clientBinding: &ClientBinding{client: &acceptanceClient{service: service}, options: client.Options{Namespace: "default-test-namespace", DataConverter: dc}}, handlers: map[string]queue.Handler{"business": func(_ context.Context, job queue.Job) error {
						calls++
						if mode == "handler panic" {
							panic("private application panic")
						}
						if string(job.PayloadBytes()) != "exact bytes" {
							return errors.New("wrong payload")
						}
						return nil
					}}}
					env := guardedWorkflowEnvironment(t)
					env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: parent, TaskQueue: "reports"})
					env.RegisterActivityWithOptions(b.executeDelivery, activity.RegisterOptions{Name: deliveryActivityType})
					env.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
						ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: "reports", ActivityID: func() string {
							if mode == "activity identity" {
								return "original-member"
							}
							return d.Metadata.JobID
						}(), StartToCloseTimeout: time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
						return workflow.ExecuteActivity(ctx, deliveryActivityType, d).Get(ctx, nil)
					}, workflow.RegisterOptions{Name: workflowType})
					env.ExecuteWorkflow(workflowType)
					err = env.GetWorkflowError()
					if mode == "valid" {
						if err != nil || calls != 1 {
							t.Fatal("valid original authority did not invoke handler", calls, err)
						}
					} else {
						var application *temporal.ApplicationError
						if !errors.As(err, &application) || strings.Contains(err.Error(), "private") {
							t.Fatal("unsafe authority failure", err)
						}
						wantCalls := 0
						if mode == "handler panic" {
							wantCalls = 1
						}
						if calls != wantCalls {
							t.Fatal("unverified authority entered handler", calls, mode)
						}
						if mode == "history outage" {
							if application.Type() != "goforj.unavailable.v1" || application.NonRetryable() {
								t.Fatal("history outage became permanent", err)
							}
						} else if mode != "closed adapters" && !application.NonRetryable() {
							t.Fatal("definite invalid authority remained retryable", err)
						}
					}
				})
			}
		})
	}
}
