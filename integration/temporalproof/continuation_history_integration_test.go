//go:build integration

package temporalproof

import (
	"strings"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
)

// continuationHistory constructs ordered proof records independently of the
// workflow implementation, including monotonic journal sequences after reordering.
func continuationHistory(t *testing.T, expected []proofIntent, steps []string) *history.History {
	t.Helper()
	recorded := &history.History{}
	sequence := 0
	for _, step := range steps {
		kind, id, _ := strings.Cut(step, ":")
		intent := proofIntent{ID: id}
		for _, known := range expected {
			if known.ID == id {
				intent = known
			}
		}
		if kind == "activity" {
			recorded.Events = append(recorded.Events, &history.HistoryEvent{
				EventType: enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
				Attributes: &history.HistoryEvent_ActivityTaskScheduledEventAttributes{
					ActivityTaskScheduledEventAttributes: &history.ActivityTaskScheduledEventAttributes{ActivityId: "continuation/" + id, WorkflowTaskCompletedEventId: 42},
				},
			})
			continue
		}
		sequence++
		payload, err := converter.GetDefaultDataConverter().ToPayload(proofLedgerDelta{Sequence: sequence, Kind: kind, Intent: intent})
		if err != nil {
			t.Fatal(err)
		}
		recorded.Events = append(recorded.Events, &history.HistoryEvent{
			EventType: enums.EVENT_TYPE_WORKFLOW_PROPERTIES_MODIFIED,
			Attributes: &history.HistoryEvent_WorkflowPropertiesModifiedEventAttributes{
				WorkflowPropertiesModifiedEventAttributes: &history.WorkflowPropertiesModifiedEventAttributes{
					WorkflowTaskCompletedEventId: 42,
					UpsertedMemo:                 &common.Memo{Fields: map[string]*common.Payload{proofLedgerKey: payload}},
				},
			},
		})
	}
	return recorded
}

// TestContinuationHistoryRejectsInvalidScheduling guards against proof false
// positives that preserve activity start order and the final outcomes-only slice.
func TestContinuationHistoryRejectsInvalidScheduling(t *testing.T) {
	expected := []proofIntent{{"progress-0", proofSnapshot{1, false}}, {"progress-1", proofSnapshot{2, true}}, {"completion", proofSnapshot{2, true}}}
	valid := []string{
		"scheduled:progress-0", "activity:progress-0", "exhausted:progress-0",
		"scheduled:progress-1", "activity:progress-1", "succeeded:progress-1",
		"scheduled:completion", "activity:completion", "succeeded:completion",
	}
	reorder := func(indices ...int) []string {
		steps := make([]string, len(indices))
		for i, index := range indices {
			steps[i] = valid[index]
		}
		return steps
	}
	for _, test := range []struct {
		name   string
		steps  []string
		mutate func(*history.History)
		want   string
	}{
		{name: "valid serial drain", steps: valid},
		{name: "later heads overlap", steps: reorder(0, 1, 2, 3, 4, 6, 7, 5, 8), want: "scheduled before"},
		{name: "duplicate schedule", steps: reorder(0, 1, 2, 3, 3, 4, 5, 6, 7, 8), want: "duplicate continuation schedule"},
		{name: "duplicate activity", steps: reorder(0, 1, 2, 3, 4, 4, 5, 6, 7, 8), want: "duplicate continuation activity"},
		{name: "missing activity", steps: reorder(0, 1, 2, 3, 5, 6, 7, 8), want: "settled before its activity"},
		{name: "missing final head", steps: reorder(0, 1, 2, 3, 4, 5), want: "missing continuation schedules"},
		{name: "missing settlement", steps: reorder(0, 1, 2, 3, 4, 5, 6, 7), want: "remains unsettled"},
		{name: "wrong head order", steps: reorder(3, 4, 5, 0, 1, 2, 6, 7, 8), want: "unexpected continuation head"},
		{name: "orphan settlement", steps: reorder(2, 0, 1, 3, 4, 5, 6, 7, 8), want: "settled without its active head"},
		{name: "duplicate settlement", steps: reorder(0, 1, 2, 2, 3, 4, 5, 6, 7, 8), want: "settled without its active head"},
		{name: "orphan activity", steps: reorder(1, 0, 2, 3, 4, 5, 6, 7, 8), want: "scheduled without its active head"},
		{name: "unknown head", steps: []string{"scheduled:unknown"}, want: "unexpected continuation head"},
		{name: "extra head after drain", steps: append(append([]string(nil), valid...), "scheduled:unknown"), want: "unexpected continuation head"},
		{name: "settlement for wrong head", steps: reorder(0, 1, 5, 2, 3, 4, 6, 7, 8), want: "settled without its active head"},
		{name: "missing commit", steps: valid, mutate: func(h *history.History) {
			h.Events[3].GetWorkflowPropertiesModifiedEventAttributes().WorkflowTaskCompletedEventId = 0
		}, want: "has no workflow-task commit"},
		{name: "different activity commit", steps: valid, mutate: func(h *history.History) {
			h.Events[4].GetActivityTaskScheduledEventAttributes().WorkflowTaskCompletedEventId++
		}, want: "different workflow-task commit"},
		{name: "malformed journal", steps: valid, mutate: func(h *history.History) {
			h.Events[3].GetWorkflowPropertiesModifiedEventAttributes().UpsertedMemo.Fields[proofLedgerKey].Data = []byte("invalid-json")
		}, want: "decode continuation ledger"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := continuationHistory(t, expected, test.steps)
			if test.mutate != nil {
				test.mutate(recorded)
			}
			err := validateContinuationSchedules(recorded, expected)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("history validation=%v, want %q", err, test.want)
			}
		})
	}
}
