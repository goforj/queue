package temporalqueue

import (
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"testing"
)

// TestHistoryRejectsSyntheticInterruptionInPersistedJournal checks that only a terminal service event can synthesize interruption.
func TestHistoryRejectsSyntheticInterruptionInPersistedJournal(t *testing.T) {
	h, def := recordedJournalFixture(t)
	mutateRecordedDelta(t, h, func(d journalDelta) bool { return d.Sequence == 8 }, func(d *journalDelta) { d.Status = "workflow-interrupted" })
	run := h.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId
	service := &acceptanceHistory{response: &workflowservice.GetWorkflowExecutionHistoryResponse{History: h}}
	sdk := &journalHistoryClient{acceptanceClient: &acceptanceClient{service: service}, description: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflow.WorkflowExecutionInfo{Execution: &common.WorkflowExecution{WorkflowId: def.ID, RunId: run}, HistoryLength: h.Events[len(h.Events)-1].EventId}}}
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	c := &coordinator{backend: &backend{entry: newAdmission(), taskQueue: def.Queue, workflowQueue: def.WorkflowQueue, clientBinding: &ClientBinding{client: sdk, options: client.Options{Namespace: "default", DataConverter: dc}}}}
	outcomes, err := c.FindContinuations(t.Context(), def.ID)
	if err == nil {
		t.Fatalf("persisted service-only status accepted: outcomes=%+v", outcomes)
	}
}
