package temporalproof

import (
	"os"
	"reflect"
	"testing"
	"time"

	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	proofJournalKey   = "queue-proof-journal"
	proofWorkflowName = "queue-proof-journal-v1"
)

type proofDelta struct {
	Version  int    `json:"version"`
	Sequence int    `json:"sequence"`
	Kind     string `json:"kind"`
}

// journalProofWorkflow emits several updates to one key before yielding so
// retained history must preserve separate deltas from the same task commit.
func journalProofWorkflow(ctx workflow.Context, input string) error {
	return journalProofBody(ctx, input, false)
}

// mutatedJournalProofWorkflow changes one memo delta without changing command
// count, demonstrating why SDK replay alone cannot qualify journal evolution.
func mutatedJournalProofWorkflow(ctx workflow.Context, input string) error {
	return journalProofBody(ctx, input, true)
}

// journalProofBody keeps the negative control's scheduling identical to the original.
func journalProofBody(ctx workflow.Context, input string, mutate bool) error {
	for sequence := 1; sequence <= 3; sequence++ {
		delta := proofDelta{Version: 1, Sequence: sequence, Kind: "pending"}
		if mutate && sequence == 2 {
			delta.Kind = "changed-with-same-command-count"
		}
		if err := workflow.UpsertMemo(ctx, map[string]any{proofJournalKey: delta}); err != nil {
			return err
		}
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Second})
	return workflow.ExecuteActivity(ctx, "proof-witness", input).Get(ctx, nil)
}

type captureJournal struct {
	interceptor.WorkerInterceptorBase
	deltas []proofDelta
}

type captureJournalInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	capture *captureJournal
}

type captureJournalOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	capture *captureJournal
}

// InterceptWorkflow captures regenerated semantic deltas only in the replay experiment.
func (c *captureJournal) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &captureJournalInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, capture: c}
}

// Init inserts semantic capture without changing SDK command scheduling.
func (c *captureJournalInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return c.Next.Init(&captureJournalOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}, capture: c.capture})
}

// UpsertMemo records regenerated values that ordinary SDK replay does not compare.
func (c *captureJournalOutbound) UpsertMemo(ctx workflow.Context, memo map[string]any) error {
	if delta, ok := memo[proofJournalKey].(proofDelta); ok {
		c.capture.deltas = append(c.capture.deltas, delta)
	}
	return c.Next.UpsertMemo(ctx, memo)
}

// committedProofDeltas reconstructs only explicit journal records and checks
// their common workflow-task commit against the scheduled witness activity.
func committedProofDeltas(t *testing.T, h *history.History, dc converter.DataConverter) []proofDelta {
	t.Helper()
	var deltas []proofDelta
	var commit int64
	var activityCommit int64
	for _, event := range h.Events {
		if attributes := event.GetWorkflowPropertiesModifiedEventAttributes(); attributes != nil {
			if commit == 0 {
				commit = attributes.WorkflowTaskCompletedEventId
			}
			if commit == 0 || commit != attributes.WorkflowTaskCompletedEventId {
				t.Fatal("same-activation deltas did not share one task commit")
			}
			var delta proofDelta
			if err := dc.FromPayload(attributes.UpsertedMemo.Fields[proofJournalKey], &delta); err != nil {
				t.Fatal(err)
			}
			deltas = append(deltas, delta)
		}
		if attributes := event.GetActivityTaskScheduledEventAttributes(); attributes != nil {
			activityCommit = attributes.WorkflowTaskCompletedEventId
		}
	}
	if commit == 0 || activityCommit != commit {
		t.Fatalf("journal/activity atomicity association: journal=%d activity=%d", commit, activityCommit)
	}
	return deltas
}

// assertSemanticReplay requires both SDK command replay and decoded journal
// equality, and demonstrates a same-command-count mutation evades SDK replay.
func assertSemanticReplay(t *testing.T, recorded *history.History, dc converter.DataConverter) {
	t.Helper()
	committed := committedProofDeltas(t, recorded, dc)
	for _, mutate := range []bool{false, true} {
		capture := &captureJournal{}
		replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{DataConverter: dc, Interceptors: []interceptor.WorkerInterceptor{capture}})
		if err != nil {
			t.Fatal(err)
		}
		implementation := journalProofWorkflow
		if mutate {
			implementation = mutatedJournalProofWorkflow
		}
		replayer.RegisterWorkflowWithOptions(implementation, workflow.RegisterOptions{Name: proofWorkflowName})
		if err := replayer.ReplayWorkflowHistory(nil, recorded); err != nil {
			t.Fatalf("mutate=%t SDK replay: %v", mutate, err)
		}
		matches := reflect.DeepEqual(capture.deltas, committed)
		if matches == mutate {
			t.Fatalf("mutate=%t semantic replay comparison=%t, regenerated=%+v", mutate, matches, capture.deltas)
		}
	}
}

// TestSyntheticJournalReplay keeps the real-server synthetic fixture useful in
// ordinary CI without requiring a live Temporal service or application code.
func TestSyntheticJournalReplay(t *testing.T) {
	encoded, err := os.ReadFile("testdata/journal-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recorded := &history.History{}
	if err := protojson.Unmarshal(encoded, recorded); err != nil {
		t.Fatal(err)
	}
	assertSemanticReplay(t, recorded, converter.GetDefaultDataConverter())
}
