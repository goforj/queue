//go:build integration

package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// readRoutingHistory retains the actual service history for route and replay assertions.
func readRoutingHistory(t *testing.T, ctx context.Context, sdk client.Client, id, run string) *history.History {
	t.Helper()
	recorded := &history.History{}
	iterator := sdk.GetWorkflowHistory(ctx, id, run, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		recorded.Events = append(recorded.Events, event)
	}
	if len(recorded.Events) == 0 || recorded.Events[0].GetWorkflowExecutionStartedEventAttributes() == nil {
		t.Fatal("accepted input is missing")
	}
	return recorded
}

// hasResourceRoutingMarker identifies the SDK version bridge rather than inferring it from successful execution.
func hasResourceRoutingMarker(recorded *history.History) bool {
	for _, event := range recorded.Events {
		marker := event.GetMarkerRecordedEventAttributes()
		if marker == nil {
			continue
		}
		payload := marker.Details["change-id"]
		if payload == nil {
			continue
		}
		var id string
		if converter.GetDefaultDataConverter().FromPayloads(payload, &id) == nil && id == "goforj.queue.resource-routing" {
			return true
		}
	}
	return false
}

// replaceActivityRoute changes only a target in an otherwise service-accepted definition.
func replaceActivityRoute(t *testing.T, input map[string]json.RawMessage, kind, target string) {
	t.Helper()
	route := func(job map[string]json.RawMessage) {
		if _, ok := job["ActivityQueue"]; !ok {
			t.Fatal("activity routing field missing")
		}
		job["ActivityQueue"] = json.RawMessage(`"unconfigured-activity-route"`)
	}
	if kind == "job" {
		route(input)
		return
	}
	field := "Jobs"
	if target == "hook" {
		field = "Hooks"
	}
	var members []map[string]json.RawMessage
	if err := json.Unmarshal(input[field], &members); err != nil || len(members) == 0 {
		t.Fatal("accepted members missing", err)
	}
	if target == "hook" {
		var job map[string]json.RawMessage
		if err := json.Unmarshal(members[0]["Job"], &job); err != nil {
			t.Fatal(err)
		}
		route(job)
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		members[0]["Job"] = raw
	} else {
		route(members[0])
	}
	raw, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	input[field] = raw
}

// TestTemporalDriverDirectResourceRoutes exercises actual SDK starts against production combined and split registrations.
func TestTemporalDriverDirectResourceRoutes(t *testing.T) {
	requireTemporalServer(t)
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprintf("split=%t", split), func(t *testing.T) {
			name := fmt.Sprintf("direct-routes-%t-%d", split, time.Now().UnixNano())
			cfg := temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress}, DefaultActivityTimeout: time.Second, ShutdownTimeout: 5 * time.Second}
			workflowQueue := name
			if split {
				cfg.WorkflowTaskQueue = name + "-workflows"
				cfg.ActivityTaskQueue = name + "-activities"
				workflowQueue = cfg.WorkflowTaskQueue
			}
			q, err := temporalqueue.NewWithConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := q.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			var calls atomic.Int32
			q.Register("route-business", func(context.Context, queue.Message) error { calls.Add(1); return nil })
			q.Register("route-hook", func(context.Context, queue.Message) error { calls.Add(1); return nil })
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			if err := q.StartWorkers(ctx); err != nil {
				t.Fatal(err)
			}
			sdk, err := temporalqueue.Client(q)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"job", "chain", "batch"} {
				var id string
				switch kind {
				case "job":
					result, err := q.WithContext(ctx).Dispatch(queue.NewJob("route-business"))
					if err != nil {
						t.Fatal(err)
					}
					id = result.DispatchID
				case "chain":
					id, err = queue.Continue(q.Chain(queue.NewJob("route-business"))).OnCompletion(queue.NewJob("route-hook")).Dispatch(ctx)
				case "batch":
					id, err = queue.Continue(q.Batch(queue.NewJob("route-business"))).OnCompletion(queue.NewJob("route-hook")).Dispatch(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := sdk.GetWorkflow(ctx, id, "").Get(ctx, nil); err != nil {
					t.Fatal(err)
				}
				original := readRoutingHistory(t, ctx, sdk, id, "")
				start := original.Events[0].GetWorkflowExecutionStartedEventAttributes()
				cases := []string{"valid", "member"}
				if kind != "job" {
					cases = append(cases, "hook")
				}
				for _, target := range cases {
					var input map[string]json.RawMessage
					if err := converter.GetDefaultDataConverter().FromPayloads(start.Input, &input); err != nil {
						t.Fatal(err)
					}
					if target != "valid" {
						replaceActivityRoute(t, input, kind, target)
					}
					before := calls.Load()
					run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: workflowQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE}, start.WorkflowType.Name, input)
					if err != nil {
						t.Fatal(err)
					}
					completion, stop := context.WithTimeout(ctx, 5*time.Second)
					err = run.Get(completion, nil)
					stop()
					recorded := readRoutingHistory(t, ctx, sdk, id, run.GetRunID())
					if target == "valid" {
						want := int32(1)
						if kind != "job" {
							want = 2
						}
						if err != nil || calls.Load() != before+want || !hasResourceRoutingMarker(recorded) {
							t.Fatal("valid direct start did not exercise production routing", kind, split, err)
						}
					} else {
						var application *temporal.ApplicationError
						if !errors.As(err, &application) || application.Type() != "goforj.invalid.v1" || !application.NonRetryable() {
							t.Fatal("direct route rejection changed", kind, target, err)
						}
						if calls.Load() != before {
							t.Fatal("invalid route reached an application handler")
						}
						for _, event := range recorded.Events {
							if event.GetActivityTaskScheduledEventAttributes() != nil || event.GetWorkflowPropertiesModifiedEventAttributes() != nil {
								t.Fatal("invalid route produced an activity or journal command", kind, target)
							}
						}
					}
				}
			}
		})
	}
}

// legacyRoutingFixture records the pre-routing-bridge command sequence using the original job body.
type legacyRoutingFixture struct {
	interceptor.WorkerInterceptorBase
}

// InterceptWorkflow retains ordinary SDK registration and initialization for the legacy command fixture.
func (*legacyRoutingFixture) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &legacyRoutingInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// legacyRoutingInbound installs only the fixture's old-version decision.
type legacyRoutingInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

// Init retains all SDK commands except the version marker absent from the prior implementation.
func (i *legacyRoutingInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&legacyRoutingOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}

// legacyRoutingOutbound emulates the former entry body without changing activity execution or other SDK versions.
type legacyRoutingOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

// GetVersion produces the prior ordinary-job command history only for this routing bridge fixture.
func (i *legacyRoutingOutbound) GetVersion(ctx workflow.Context, id string, min, max workflow.Version) workflow.Version {
	if id == "goforj.queue.resource-routing" {
		return workflow.DefaultVersion
	}
	return i.Next.GetVersion(ctx, id, min, max)
}

// TestTemporalDriverLegacyJobRoutingReplay captures real legacy/current command histories and replays both with the production bridge.
func TestTemporalDriverLegacyJobRoutingReplay(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("legacy-job-routing-%d", time.Now().UnixNano())
	first := newDriverResource(t, name, nil, worker.Options{Interceptors: []interceptor.WorkerInterceptor{&legacyRoutingFixture{}}})
	var calls atomic.Int32
	first.Register("legacy-job", func(context.Context, queue.Message) error { calls.Add(1); return nil })
	if err := first.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := first.WithContext(t.Context()).Dispatch(queue.NewJob("legacy-job"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.GetWorkflow(t.Context(), accepted.DispatchID, "").Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	legacy := readRoutingHistory(t, t.Context(), sdk, accepted.DispatchID, "")
	if hasResourceRoutingMarker(legacy) {
		t.Fatal("legacy command fixture contains new routing marker")
	}
	if err := first.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := newDriverResource(t, name, nil)
	current.Register("legacy-job", func(context.Context, queue.Message) error { calls.Add(1); return nil })
	replay, err := temporalqueue.NewWorkflowReplayer(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayWorkflowHistory(nil, legacy); err != nil {
		t.Fatal("legacy ordinary history did not replay", err)
	}
	if err := current.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	sdk, err = temporalqueue.Client(current)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err = current.WithContext(t.Context()).Dispatch(queue.NewJob("legacy-job"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.GetWorkflow(t.Context(), accepted.DispatchID, "").Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	recorded := readRoutingHistory(t, t.Context(), sdk, accepted.DispatchID, "")
	if !hasResourceRoutingMarker(recorded) {
		t.Fatal("current ordinary history has no routing marker")
	}
	if err := replay.ReplayWorkflowHistory(nil, recorded); err != nil {
		t.Fatal("current routing history did not replay", err)
	}
	if calls.Load() != 2 {
		t.Fatal("replay repeated an application effect", calls.Load())
	}
}
