package temporalqueue

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

// semanticCapture compares regenerated production deltas because SDK replay only checks memo command shape.
type semanticCapture struct {
	interceptor.WorkerInterceptorBase
	deltas []journalDelta
	mutate bool
}

// semanticInbound installs the read-only qualification capture on the actual coordinator.
type semanticInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	capture *semanticCapture
}

// semanticOutbound retains memo command count while the negative control changes one semantic field.
type semanticOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	capture *semanticCapture
}

// InterceptWorkflow retains native workflow scheduling during qualification.
func (c *semanticCapture) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &semanticInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, capture: c}
}

// Init places semantic capture around actual production journal publication.
func (i *semanticInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&semanticOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}, capture: i.capture})
}

// UpsertMemo records only the payload-free persisted representation, with an optional content-only mutation.
func (o *semanticOutbound) UpsertMemo(ctx workflow.Context, memo map[string]any) error {
	if value, ok := memo[journalKey].(journalValue); ok {
		if o.capture.mutate && len(o.capture.deltas) == 0 {
			value.Delta.At = value.Delta.At.Add(time.Second)
			memo[journalKey] = value
		}
		raw, err := json.Marshal(value.Delta)
		if err != nil {
			return err
		}
		var persisted journalDelta
		if err := json.Unmarshal(raw, &persisted); err != nil {
			return err
		}
		o.capture.deltas = append(o.capture.deltas, persisted)
	}
	return o.Next.UpsertMemo(ctx, memo)
}

// TestProductionSemanticReplay qualifies retained original production history and rejects a same-command-count mutation.
func TestProductionSemanticReplay(t *testing.T) {
	data, err := os.ReadFile("testdata/coordinator-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recorded := &history.History{}
	if err := protojson.Unmarshal(data, recorded); err != nil {
		t.Fatal(err)
	}
	var def workflowDefinition
	if err := converter.GetDefaultDataConverter().FromPayloads(recorded.Events[0].GetWorkflowExecutionStartedEventAttributes().Input, &def); err != nil {
		t.Fatal(err)
	}
	var committed []journalDelta
	for _, event := range recorded.Events {
		if change := event.GetWorkflowPropertiesModifiedEventAttributes(); change != nil {
			var delta journalDelta
			if err := converter.GetDefaultDataConverter().FromPayload(change.UpsertedMemo.Fields[journalKey], &delta); err != nil {
				t.Fatal(err)
			}
			committed = append(committed, delta)
		}
	}
	if len(committed) == 0 {
		t.Fatal("fixture has no production journal")
	}
	for _, mutate := range []bool{false, true} {
		capture := &semanticCapture{mutate: mutate}
		cfg := Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: def.Queue}, DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second}
		cfg.WorkerOptions.Interceptors = []interceptor.WorkerInterceptor{capture}
		q, err := NewWithConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := NewWorkflowReplayer(q)
		if err != nil {
			t.Fatal(err)
		}
		if err := replay.ReplayWorkflowHistory(nil, recorded); err != nil {
			t.Fatalf("mutate=%t SDK replay failed: %v", mutate, err)
		}
		if equal := reflect.DeepEqual(capture.deltas, committed); equal == mutate {
			t.Fatalf("mutate=%t semantic equality=%t", mutate, equal)
		}
		if err := q.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
