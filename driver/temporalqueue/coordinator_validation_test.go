package temporalqueue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	engine "github.com/goforj/queue/internal/workflow"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// TestCoordinatorRejectsMalformedAcceptedDefinitions covers validation of persisted policies independently of producer builders.
func TestCoordinatorRejectsMalformedAcceptedDefinitions(t *testing.T) {
	cases := map[string]func(*workflowDefinition){
		"journal lower bound":            func(d *workflowDefinition) { d.JournalBudget = 1023 },
		"journal upper bound":            func(d *workflowDefinition) { d.JournalBudget = (2 << 20) + 1 },
		"negative encoding overhead":     func(d *workflowDefinition) { d.EncodingOverhead = -1 },
		"encoding overhead overflow":     func(d *workflowDefinition) { d.EncodingOverhead = 4097 },
		"journal cannot hold transition": func(d *workflowDefinition) { d.JournalBudget = 1024 },
		"version":                        func(d *workflowDefinition) { d.Version = 2 },
		"kind":                           func(d *workflowDefinition) { d.Kind = "unknown" },
		"creation time":                  func(d *workflowDefinition) { d.CreatedAt = time.Time{} },
		"identity":                       func(d *workflowDefinition) { d.ID = "" },
		"dispatch identity":              func(d *workflowDefinition) { d.DispatchID = "" },
		"empty members":                  func(d *workflowDefinition) { d.Jobs = nil },
		"name bound":                     func(d *workflowDefinition) { d.Name = strings.Repeat("x", 257) },
		"member policy":                  func(d *workflowDefinition) { d.Jobs[0].Resolved.Timeout = 0 },
		"member correlation":             func(d *workflowDefinition) { d.Jobs[0].Metadata.DispatchID = "different" },
		"duplicate member":               func(d *workflowDefinition) { d.Jobs = append(d.Jobs, d.Jobs[0]) },
		"member workflow route":          func(d *workflowDefinition) { d.Jobs[0].WorkflowQueue = "different" },
		"hook phase":                     func(d *workflowDefinition) { d.Hooks[0].Phase = "invalid" },
		"hook policy":                    func(d *workflowDefinition) { d.Hooks[0].Job.Resolved.Timeout = 0 },
		"hook correlation":               func(d *workflowDefinition) { d.Hooks[0].Job.Metadata.DispatchID = "different" },
		"definition byte budget": func(d *workflowDefinition) {
			d.Hooks = nil
			for i := range d.Jobs {
				d.Jobs[i].Payload = make([]byte, 100<<10)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, def := recordedJournalFixture(t)
			if err := def.validate(); err != nil {
				t.Fatal("invalid control fixture", err)
			}
			mutate(&def)
			if err := def.validate(); err == nil {
				t.Fatal("malformed persisted definition accepted")
			}
		})
	}
}

// coordinatorAcceptanceFixture captures service acceptance separately from local validation and converter preflight.
func coordinatorAcceptanceFixture(t *testing.T) (*coordinator, *acceptanceClient) {
	t.Helper()
	limits := DefaultSerializationLimits()
	dc, err := newGuardedConverter(nil, limits, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	sdk := &acceptanceClient{errors: []error{nil}}
	b := &backend{cfg: testConfig(), entry: newAdmission(), gate: newAdmission(), taskQueue: "reports", workflowQueue: "reports", activityQueue: "reports", clientBinding: &ClientBinding{client: sdk, limits: limits, options: client.Options{Namespace: "default", DataConverter: dc}}}
	return &coordinator{backend: b}, sdk
}

// TestCoordinatorPreflightRejectsBeforeServiceAcceptance verifies errors do not produce hidden accepted workflows.
func TestCoordinatorPreflightRejectsBeforeServiceAcceptance(t *testing.T) {
	for _, scenario := range []string{"closed", "workflow route", "member route", "member uniqueness", "member encoding", "member timeout", "member type", "hook route", "hook uniqueness", "hook timeout", "hook phase", "journal encoding"} {
		t.Run(scenario, func(t *testing.T) {
			c, sdk := coordinatorAcceptanceFixture(t)
			queueName := "reports"
			jobs := []engine.Job{{Type: "business"}}
			hooks := []engine.ContinuationDefinition{{Phase: "completion", Job: engine.StoredJob{Type: "hook"}}}
			switch scenario {
			case "closed":
				c.backend.entry.close()
			case "workflow route":
				queueName = "unknown"
			case "member route":
				jobs[0].Options.Queue = "unknown"
			case "member uniqueness":
				jobs[0].Options.UniqueFor = time.Second
			case "member encoding":
				jobs[0].Payload = make(chan int)
			case "member timeout":
				jobs[0].Options.Timeout = -time.Second
			case "member type":
				jobs[0].Type = "bus:reserved"
			case "hook route":
				hooks[0].Job.Options.Queue = "unknown"
			case "hook uniqueness":
				hooks[0].Job.Options.UniqueFor = time.Second
			case "hook timeout":
				hooks[0].Job.Options.Timeout = -time.Second
			case "hook phase":
				hooks[0].Phase = "progress"
			case "journal encoding":
				dc, err := newGuardedConverter(&journalFailureConverter{DataConverter: converter.GetDefaultDataConverter(), failure: errors.New("private codec diagnostic")}, DefaultSerializationLimits(), newAdmission())
				if err != nil {
					t.Fatal(err)
				}
				c.backend.clientBinding.options.DataConverter = dc
			}
			id, err := c.start(t.Context(), "chain", "", queueName, false, jobs, hooks)
			if err == nil || id != "" || len(sdk.ids) != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("preflight crossed acceptance: id=%s error=%v calls=%v", id, err, sdk.ids)
			}
		})
	}
	for _, payload := range []any{json.RawMessage(`{"id":42}`), []byte("opaque bytes"), map[string]int{"id": 42}} {
		t.Run("accepted payload", func(t *testing.T) {
			c, sdk := coordinatorAcceptanceFixture(t)
			id, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business", Payload: payload}}, nil)
			if err != nil || id == "" || len(sdk.inputs) != 1 {
				t.Fatalf("valid workflow rejected: %s %v", id, err)
			}
			accepted := sdk.inputs[0].(workflowDefinition)
			if accepted.ID != id || accepted.Jobs[0].Metadata.ChainID != id || accepted.Jobs[0].Resolved.Timeout != time.Second {
				t.Fatal("accepted identity/policy changed")
			}
		})
	}
	c, sdk := coordinatorAcceptanceFixture(t)
	sdk.errors = []error{context.Canceled, context.Canceled}
	id, err := c.start(t.Context(), "chain", "", "", false, []engine.Job{{Type: "business"}}, nil)
	if id == "" || err == nil {
		t.Fatal("post-submission uncertainty lost its identity", id, err)
	}
}

// TestTemporalBuildersRejectEphemeralHooksAndUnknownRoutes checks the public chain and batch adapters before any worker exists.
func TestTemporalBuildersRejectEphemeralHooksAndUnknownRoutes(t *testing.T) {
	q, err := NewWithConfig(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(t.Context())
	callbacks := []struct {
		name     string
		dispatch func() (string, error)
	}{
		{"chain failure", func() (string, error) {
			return q.Chain(queue.NewJob("job")).Catch(func(context.Context, queue.ChainState, error) error { return nil }).Dispatch(t.Context())
		}},
		{"chain finally", func() (string, error) {
			return q.Chain(queue.NewJob("job")).Finally(func(context.Context, queue.ChainState) error { return nil }).Dispatch(t.Context())
		}},
		{"chain route", func() (string, error) { return q.Chain(queue.NewJob("job")).OnQueue("unknown").Dispatch(t.Context()) }},
		{"batch progress", func() (string, error) {
			return q.Batch(queue.NewJob("job")).Progress(func(context.Context, queue.BatchState) error { return nil }).Dispatch(t.Context())
		}},
		{"batch success", func() (string, error) {
			return q.Batch(queue.NewJob("job")).Then(func(context.Context, queue.BatchState) error { return nil }).Dispatch(t.Context())
		}},
		{"batch failure", func() (string, error) {
			return q.Batch(queue.NewJob("job")).Catch(func(context.Context, queue.BatchState, error) error { return nil }).Dispatch(t.Context())
		}},
		{"batch finally", func() (string, error) {
			return q.Batch(queue.NewJob("job")).Finally(func(context.Context, queue.BatchState) error { return nil }).Dispatch(t.Context())
		}},
		{"batch route", func() (string, error) { return q.Batch(queue.NewJob("job")).OnQueue("unknown").Dispatch(t.Context()) }},
	}

	for _, item := range callbacks {
		t.Run(item.name, func(t *testing.T) {
			if id, err := item.dispatch(); err == nil || id != "" {
				t.Fatal("unsupported builder accepted", id, err)
			}
		})
	}
	if err := q.Prune(t.Context(), time.Now()); !errors.Is(err, queue.ErrWorkflowUnsupported) {
		t.Fatal("portable history pruning lost unsupported capability category", err)
	}
}
