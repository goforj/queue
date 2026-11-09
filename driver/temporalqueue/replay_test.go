package temporalqueue

import (
	"context"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/queue"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// TestReplayJSONRetainsOriginalResourceAndPartialHistory exercises the public capture methods against the production retained fixture.
func TestReplayJSONRetainsOriginalResourceAndPartialHistory(t *testing.T) {
	recorded, def := recordedJournalFixture(t)
	cfg := testConfig()
	cfg.DefaultQueue = def.Queue
	q, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(t.Context())
	replay, err := NewWorkflowReplayer(q)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "retained.json")
	raw, err := protojson.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayWorkflowHistoryFromJSONFile(nil, path); err != nil {
		t.Fatal("retained capture did not replay", err)
	}
	capture := &replayHistoryCapture{WorkflowReplayer: replay.(*resourceReplayer).WorkflowReplayer}
	replay.(*resourceReplayer).WorkflowReplayer = capture
	cutoff := recorded.Events[len(recorded.Events)-2].EventId
	if err := replay.ReplayPartialWorkflowHistoryFromJSONFile(nil, path, cutoff); err != nil {
		t.Fatal("bounded partial capture did not replay", err)
	}
	if capture.events != len(recorded.Events)-1 || capture.last != cutoff {
		t.Fatalf("partial cutoff ignored: events=%d last=%d cutoff=%d", capture.events, capture.last, cutoff)
	}
	for name, path := range map[string]string{"missing": filepath.Join(t.TempDir(), "absent"), "invalid": filepath.Join(t.TempDir(), "bad.json")} {
		t.Run(name, func(t *testing.T) {
			if name == "invalid" {
				if err := os.WriteFile(path, []byte("invalid JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := replay.ReplayWorkflowHistoryFromJSONFile(nil, path); err == nil {
				t.Fatal("invalid capture accepted")
			}
		})
	}
	if err := replay.ReplayWorkflowExecution(t.Context(), nil, nil, "different namespace", workflow.Execution{}); err == nil {
		t.Fatal("cross-namespace replay accepted")
	}
	if err := q.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayWorkflowHistory(nil, recorded); err == nil {
		t.Fatal("replay touched closed resource")
	}
	if _, err := NewWorkflowReplayer(q); err == nil {
		t.Fatal("replayer created after shutdown")
	}
}

// TestReplayRejectsMalformedOriginalHistory prevents invalid sequences and absent execution identity from reaching SDK replay.
func TestReplayRejectsMalformedOriginalHistory(t *testing.T) {
	recorded, _ := recordedJournalFixture(t)
	q, err := NewWithConfig(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(t.Context())
	replayer, err := NewWorkflowReplayer(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nil", "empty", "count", "nil event", "zero event", "duplicate sequence", "missing start", "missing workflow identity"} {
		t.Run(name, func(t *testing.T) {
			value := proto.Clone(recorded).(*history.History)
			switch name {
			case "nil":
				value = nil
			case "empty":
				value.Events = nil
			case "count":
				value.Events = make([]*history.HistoryEvent, 100001)
			case "nil event":
				value.Events[0] = nil
			case "zero event":
				value.Events[0].EventId = 0
			case "duplicate sequence":
				value.Events[1].EventId = value.Events[0].EventId
			case "missing start":
				value.Events[0].Attributes = nil
			case "missing workflow identity":
				value.Events[0].GetWorkflowExecutionStartedEventAttributes().WorkflowId = ""
			}
			if err := replayer.ReplayWorkflowHistory(nil, value); err == nil {
				t.Fatal("invalid replay history accepted")
			}
		})
	}
	ordinary, err := queue.NewSync()
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Shutdown(t.Context())
	if _, err := NewWorkflowReplayer(ordinary); err == nil {
		t.Fatal("non-Temporal resource exposed SDK replayer")
	}
}

// replayHistoryCapture observes the actual history passed to SDK execution replay, then delegates to the real SDK replayer.
type replayHistoryCapture struct {
	worker.WorkflowReplayer
	events int
	last   int64
}

// ReplayWorkflowExecution verifies truncation through the service boundary rather than merely accepting another full replay.
func (c *replayHistoryCapture) ReplayWorkflowExecution(ctx context.Context, service workflowservice.WorkflowServiceClient, logger log.Logger, namespace string, execution workflow.Execution) error {
	response, err := service.GetWorkflowExecutionHistory(ctx, &workflowservice.GetWorkflowExecutionHistoryRequest{})
	if err != nil {
		return err
	}
	c.events = len(response.History.Events)
	c.last = response.History.Events[c.events-1].EventId
	return c.WorkflowReplayer.ReplayWorkflowExecution(ctx, service, logger, namespace, execution)
}
