package temporalqueue

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/goforj/queue/internal/workflow"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
)

// journalHistoryClient keeps state validation worker-independent while allowing authoritative history mutations.
type journalHistoryClient struct {
	*acceptanceClient
	description *workflowservice.DescribeWorkflowExecutionResponse
	describeErr error
}

// DescribeWorkflowExecution pins the original retained run and a complete watermark before any reduction.
func (c *journalHistoryClient) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return c.description, c.describeErr
}

// recordedJournalFixture decodes only synthetic retained history, leaving validation to the actual reader.
func recordedJournalFixture(t *testing.T) (*history.History, workflowDefinition) {
	t.Helper()
	raw, err := os.ReadFile("testdata/coordinator-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recorded := &history.History{}
	if err := protojson.Unmarshal(raw, recorded); err != nil {
		t.Fatal(err)
	}
	var def workflowDefinition
	if err := converter.GetDefaultDataConverter().FromPayloads(recorded.Events[0].GetWorkflowExecutionStartedEventAttributes().Input, &def); err != nil {
		t.Fatal(err)
	}
	return recorded, def
}

// mutateRecordedDelta changes one matching committed journal value while retaining event and command count.
func mutateRecordedDelta(t *testing.T, recorded *history.History, match func(journalDelta) bool, mutate func(*journalDelta)) {
	t.Helper()
	dc := converter.GetDefaultDataConverter()
	for _, event := range recorded.Events {
		if change := event.GetWorkflowPropertiesModifiedEventAttributes(); change != nil {
			var delta journalDelta
			if err := dc.FromPayload(change.UpsertedMemo.Fields[journalKey], &delta); err != nil {
				t.Fatal(err)
			}
			if match(delta) {
				mutate(&delta)
				payload, err := dc.ToPayload(delta)
				if err != nil {
					t.Fatal(err)
				}
				change.UpsertedMemo.Fields[journalKey] = payload
				return
			}
		}
	}
	t.Fatal("fixture has no matching journal transition")
}

// TestHistoryReaderRejectsMutatedCommittedProtocol exercises the public state boundary against content-only history corruption.
func TestHistoryReaderRejectsMutatedCommittedProtocol(t *testing.T) {
	member := func(d journalDelta) bool { return d.Index >= 0 }
	intent := func(d journalDelta) bool { return len(d.Intents) > 0 }
	scheduled := func(d journalDelta) bool { return d.Status == "scheduled" }
	settled := func(d journalDelta) bool { return d.Status == "succeeded" }
	cases := map[string]func(*testing.T, *history.History){
		"member outside catalog": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, member, func(d *journalDelta) { d.Index = 999 })
		},
		"sequence gap": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, member, func(d *journalDelta) { d.Sequence++ })
		},
		"duplicate member": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, func(d journalDelta) bool { return d.Index == 1 }, func(d *journalDelta) { d.Index = 0 })
		},
		"missing intent": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, intent, func(d *journalDelta) { d.Intents = nil })
		},
		"false phase": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, intent, func(d *journalDelta) { d.Intents[0].Outcome.Invocation.Phase = "failure" })
		},
		"false invocation sequence": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, intent, func(d *journalDelta) { d.Intents[0].Outcome.Invocation.Sequence++ })
		},
		"forged snapshot": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, intent, func(d *journalDelta) { d.Intents[0].Outcome.Invocation.Trigger.Pending++ })
		},
		"unknown declaration": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, intent, func(d *journalDelta) { d.Intents[0].Outcome.Invocation.Declaration = 999 })
		},
		"head substitution": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, settled, func(d *journalDelta) { d.InvocationID = "another-invocation" })
		},
		"unknown settlement": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, settled, func(d *journalDelta) { d.Status = "invented" })
		},
		"settlement before scheduling": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, scheduled, func(d *journalDelta) { d.Status = "succeeded" })
		},
		"canceled terminal member": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, member, func(d *journalDelta) { d.Skipped = true; d.Terminal = true })
		},
		"false aggregate": func(t *testing.T, h *history.History) {
			mutateRecordedDelta(t, h, member, func(d *journalDelta) { d.Cancelled = true })
		},
		"foreign type": func(_ *testing.T, h *history.History) {
			h.Events[0].GetWorkflowExecutionStartedEventAttributes().WorkflowType.Name = "foreign"
		},
		"reset lineage": func(_ *testing.T, h *history.History) {
			h.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId = "another-run"
		},
		"incomplete prefix": func(_ *testing.T, h *history.History) { h.Events = h.Events[:len(h.Events)-1] },
		"event gap":         func(_ *testing.T, h *history.History) { h.Events[1].EventId++ },
	}
	cases["valid control"] = func(*testing.T, *history.History) {}
	cases["absent definition"] = func(_ *testing.T, h *history.History) { h.Events = h.Events[:1]; h.Events[0].Attributes = nil }
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h, def := recordedJournalFixture(t)
			original := h.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId
			watermark := h.Events[len(h.Events)-1].EventId
			mutate(t, h)
			if name == "absent definition" {
				watermark = 1
			}
			service := &acceptanceHistory{response: &workflowservice.GetWorkflowExecutionHistoryResponse{History: h}}
			c := &journalHistoryClient{acceptanceClient: &acceptanceClient{service: service}, description: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &common.WorkflowExecution{WorkflowId: def.ID, RunId: original}, HistoryLength: watermark}}}
			dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			coordinator := &coordinator{backend: &backend{entry: newAdmission(), taskQueue: def.Queue, workflowQueue: def.WorkflowQueue, clientBinding: &ClientBinding{client: c, options: client.Options{Namespace: "default", DataConverter: dc}}}}
			state, err := coordinator.FindBatch(t.Context(), def.ID)
			if name == "valid control" {
				if err != nil || !state.Completed || state.Processed != 2 {
					t.Fatalf("valid original state %+v %v", state, err)
				}
			} else if err == nil {
				t.Fatalf("mutated protocol accepted: %+v", state)
			} else if errors.Is(err, workflow.ErrNotFound) {
				t.Fatal("corrupt protocol became not-found")
			}
		})
	}
}

// TestHistoryReadFailuresKeepServiceCategories distinguishes authorization, outages, absence and invalid requests without private diagnostics.
func TestHistoryReadFailuresKeepServiceCategories(t *testing.T) {
	for _, scenario := range []string{"denied", "outage", "missing", "invalid", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var original error
			switch scenario {
			case "denied":
				original = serviceerror.NewPermissionDenied("private server sentinel", "")
			case "outage":
				original = serviceerror.NewUnavailable("private server sentinel")
			case "missing":
				original = serviceerror.NewNotFound("private server sentinel")
			case "invalid":
				original = serviceerror.NewInvalidArgument("private server sentinel")
			case "canceled":
				cancel()
				original = serviceerror.NewUnavailable("private server sentinel")
			}
			err := historyReadError(ctx, original)
			if strings.Contains(err.Error(), "private") {
				t.Fatal("private RPC diagnostic exposed")
			}
			var denied *serviceerror.PermissionDenied
			var unavailable *serviceerror.Unavailable
			var invalid *serviceerror.InvalidArgument
			switch scenario {
			case "denied":
				if !errors.As(err, &denied) {
					t.Fatalf("authorization lost: %v", err)
				}
			case "outage":
				if !errors.As(err, &unavailable) {
					t.Fatalf("availability lost: %v", err)
				}
			case "missing":
				if !errors.Is(err, workflow.ErrNotFound) {
					t.Fatalf("absence lost: %v", err)
				}
			case "invalid":
				if !errors.As(err, &invalid) {
					t.Fatalf("invalid request lost: %v", err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			}
		})
	}
}

// TestAdmittedHistoryReadSurvivesEntryClosure preserves internal authority checks during graceful callback drain.
func TestAdmittedHistoryReadSurvivesEntryClosure(t *testing.T) {
	h, def := recordedJournalFixture(t)
	run := h.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId
	service := &acceptanceHistory{response: &workflowservice.GetWorkflowExecutionHistoryResponse{History: h}}
	sdkClient := &journalHistoryClient{acceptanceClient: &acceptanceClient{service: service}, description: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &common.WorkflowExecution{WorkflowId: def.ID, RunId: run}, HistoryLength: h.Events[len(h.Events)-1].EventId}}}
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	b := &backend{entry: newAdmission(), taskQueue: def.Queue, workflowQueue: def.WorkflowQueue, clientBinding: &ClientBinding{client: sdkClient, options: client.Options{Namespace: "default", DataConverter: dc}}}
	release, err := b.entry.enter()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	b.entry.close()
	c := &coordinator{backend: b}
	if _, _, _, err := c.readStateRunAdmitted(t.Context(), def.ID, def.Kind, run); err != nil {
		t.Fatalf("admitted authority read rejected during drain: %v", err)
	}
	if _, _, _, err := c.readStateRun(t.Context(), def.ID, def.Kind, run); !errors.Is(err, errAdmissionClosed) {
		t.Fatalf("new public read bypassed closure: %v", err)
	}
}
