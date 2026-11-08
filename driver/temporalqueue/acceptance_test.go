package temporalqueue

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc"
)

// acceptanceClient makes response loss independent of server-side acceptance in focused protocol checks.
type acceptanceClient struct {
	client.Client
	errors  []error
	ids     []string
	inputs  []any
	service workflowservice.WorkflowServiceClient
}

// ExecuteWorkflow captures every immutable retry request and supplies the scripted response.
func (c *acceptanceClient) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{}) (client.WorkflowRun, error) {
	c.ids = append(c.ids, options.ID)
	c.inputs = append(c.inputs, args[0])
	err := c.errors[0]
	c.errors = c.errors[1:]
	return nil, err
}

// WorkflowService exposes only the bounded original-start read used by reconciliation.
func (c *acceptanceClient) WorkflowService() workflowservice.WorkflowServiceClient { return c.service }

// acceptanceHistory supplies either retained original input or a read-permission/retention failure.
type acceptanceHistory struct {
	workflowservice.WorkflowServiceClient
	response *workflowservice.GetWorkflowExecutionHistoryResponse
	err      error
}

// GetWorkflowExecutionHistory models the original retained execution without any worker query.
func (s *acceptanceHistory) GetWorkflowExecutionHistory(context.Context, *workflowservice.GetWorkflowExecutionHistoryRequest, ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	return s.response, s.err
}

// TestAcceptanceRetainsUncertaintyAcrossRetries prevents later failures from disproving an earlier ambiguous commit.
func TestAcceptanceRetainsUncertaintyAcrossRetries(t *testing.T) {
	for name, errs := range map[string][]error{
		"unavailable then forbidden": {serviceerror.NewUnavailable("private"), serviceerror.NewPermissionDenied("private", "")},
		"canceled":                   {serviceerror.NewCanceled("private"), serviceerror.NewCanceled("private")},
		"internal":                   {serviceerror.NewInternal("private"), serviceerror.NewInvalidArgument("private")},
	} {
		t.Run(name, func(t *testing.T) {
			c := &acceptanceClient{errors: errs}
			b := backend{clientBinding: &ClientBinding{client: c}, workflowQueue: "queue"}
			input := delivery{Type: "original"}
			if err := b.acceptWorkflow(t.Context(), "stable", jobWorkflowType, input); !errors.Is(err, ErrAcceptanceUnknown) {
				t.Fatalf("lost uncertainty %v", err)
			}
			if !reflect.DeepEqual(c.ids, []string{"stable", "stable"}) || !reflect.DeepEqual(c.inputs[0], c.inputs[1]) {
				t.Fatal("retry changed identity or input")
			}
		})
	}
}

// TestAcceptanceReconcilesOnlyMatchingRetainedInput rejects collisions and keeps unreadable history unresolved.
func TestAcceptanceReconcilesOnlyMatchingRetainedInput(t *testing.T) {
	input := delivery{Version: 1, Type: "original"}
	payload, err := converter.GetDefaultDataConverter().ToPayloads(input)
	if err != nil {
		t.Fatal(err)
	}
	start := &history.WorkflowExecutionStartedEventAttributes{WorkflowType: &common.WorkflowType{Name: jobWorkflowType}, TaskQueue: &taskqueue.TaskQueue{Name: "queue"}, OriginalExecutionRunId: "run", FirstExecutionRunId: "run", Attempt: 1, Input: payload}
	response := &workflowservice.GetWorkflowExecutionHistoryResponse{History: &history.History{Events: []*history.HistoryEvent{{EventId: 1, Attributes: &history.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: start}}}}}
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"matching", "collision", "forbidden", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			service := &acceptanceHistory{response: response}
			expected := input
			switch scenario {
			case "collision":
				expected.Type = "different"
			case "forbidden":
				service.err = serviceerror.NewPermissionDenied("private", "")
			case "expired":
				service.err = serviceerror.NewNotFound("expired")
			}
			c := &acceptanceClient{service: service}
			b := backend{workflowQueue: "queue", clientBinding: &ClientBinding{client: c, options: client.Options{Namespace: "default", DataConverter: dc}}}
			err := b.reconcileWorkflow(t.Context(), "stable", "run", jobWorkflowType, expected)
			if scenario == "matching" && err != nil {
				t.Fatal(err)
			}
			if scenario == "collision" && (err == nil || errors.Is(err, ErrAcceptanceUnknown)) {
				t.Fatalf("collision %v", err)
			}
			if (scenario == "forbidden" || scenario == "expired") && !errors.Is(err, ErrAcceptanceUnknown) {
				t.Fatalf("invented acceptance %v", err)
			}
		})
	}
}
