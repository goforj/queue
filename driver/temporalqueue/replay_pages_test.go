package temporalqueue

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
)

// replayPagesFixture supplies bounded, local pagination cases without a remote service.
type replayPagesFixture struct {
	workflowservice.WorkflowServiceClient
	calls int
	page  func(int) (*workflowservice.GetWorkflowExecutionHistoryResponse, error)
}

// GetWorkflowExecutionHistory exposes the number of admitted pages and checks the resolved resource identity.
func (f *replayPagesFixture) GetWorkflowExecutionHistory(_ context.Context, request *workflowservice.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	f.calls++
	if request.Namespace != "default" || request.Execution.WorkflowId != "workflow" || request.Execution.RunId != "run" || request.MaximumPageSize != 128 {
		return nil, errors.New("replay request lost resource identity or page bound")
	}
	return f.page(f.calls)
}

// replayAdmissionCapture records only histories admitted to the SDK replay engine.
type replayAdmissionCapture struct {
	worker.WorkflowReplayer
	calls  int
	events []*history.HistoryEvent
}

// ReplayWorkflowExecution verifies the bounded assembled history rather than fetching more remote pages.
func (c *replayAdmissionCapture) ReplayWorkflowExecution(ctx context.Context, service workflowservice.WorkflowServiceClient, _ log.Logger, namespace string, execution workflow.Execution) error {
	c.calls++
	response, err := service.GetWorkflowExecutionHistory(ctx, &workflowservice.GetWorkflowExecutionHistoryRequest{})
	if err != nil {
		return err
	}
	if namespace != "default" || execution.ID != "workflow" || execution.RunID != "run" {
		return errors.New("SDK replay identity changed")
	}
	c.events = response.GetHistory().GetEvents()
	return nil
}

// TestServiceReplayHistoryAdmission applies the same limits to every replay entrypoint before SDK interpretation.
func TestServiceReplayHistoryAdmission(t *testing.T) {
	for _, mode := range []string{"normal", "repeat", "cycle", "pages", "raw", "absent", "events", "bytes", "error"} {
		t.Run(mode, func(t *testing.T) {
			binding := &ClientBinding{options: client.Options{Namespace: "default"}, gate: newAdmission()}
			capture := &replayAdmissionCapture{}
			replayer := &resourceReplayer{WorkflowReplayer: capture, binding: binding}
			fixture := &replayPagesFixture{}
			fixture.page = func(index int) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
				response := &workflowservice.GetWorkflowExecutionHistoryResponse{History: &history.History{}}
				switch mode {
				case "normal":
					response.History.Events = []*history.HistoryEvent{{EventId: int64(index)}}
					if index == 1 {
						response.NextPageToken = []byte("next")
					}
				case "repeat":
					response.NextPageToken = []byte("same")
				case "cycle":
					response.NextPageToken = []byte(fmt.Sprint(index % 2))
				case "pages":
					response.NextPageToken = []byte(fmt.Sprint(index))
				case "raw":
					response.RawHistory = []*common.DataBlob{{Data: []byte("opaque")}}
				case "absent":
					return nil, nil
				case "events":
					event := &history.HistoryEvent{EventId: 1}
					response.History.Events = make([]*history.HistoryEvent, maxReplayHistoryEvents+1)
					for i := range response.History.Events {
						response.History.Events[i] = event
					}
				case "bytes":
					event := &history.HistoryEvent{Attributes: &history.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &history.WorkflowExecutionStartedEventAttributes{Input: &common.Payloads{Payloads: []*common.Payload{{Data: make([]byte, 1<<20)}}}}}}
					for range 65 {
						response.History.Events = append(response.History.Events, event)
					}
				case "error":
					return nil, errors.New("synthetic service failure")
				}
				return response, nil
			}
			err := replayer.ReplayWorkflowExecution(t.Context(), fixture, nil, "default", workflow.Execution{ID: "workflow", RunID: "run"})
			wantPages := 1
			switch mode {
			case "normal", "repeat":
				wantPages = 2
			case "cycle":
				wantPages = 3
			case "pages":
				wantPages = 1000
			}
			if fixture.calls != wantPages {
				t.Fatal("pagination was not bounded", fixture.calls, wantPages, err)
			}
			if mode == "normal" {
				if err != nil || capture.calls != 1 || len(capture.events) != 2 || capture.events[0].EventId != 1 || capture.events[1].EventId != 2 {
					t.Fatal("valid multi-page history changed", capture.calls, capture.events, err)
				}
			} else if err == nil || capture.calls != 0 {
				t.Fatal("invalid service history reached SDK replay", capture.calls, err)
			}
		})
	}
}
