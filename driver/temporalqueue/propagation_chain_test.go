package temporalqueue

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// composedHeaderKey keeps each provider's extracted context independently observable.
type composedHeaderKey string

// composedHeaderFixture supplies individually valid headers whose combined budget is controlled by each case.
type composedHeaderFixture struct {
	prefix, value string
	count, bytes  int
}

// MaxDecodedBytes gives the composite a concrete sum of independent adapter declarations.
func (*composedHeaderFixture) MaxDecodedBytes() int { return 1024 }

// Inject preserves ordinary SDK last-writer behavior for overlapping keys.
func (p *composedHeaderFixture) Inject(_ context.Context, writer workflow.HeaderWriter) error {
	for i := range p.count {
		writer.Set(fmt.Sprintf("%s%d", p.prefix, i), &common.Payload{Data: []byte(strings.Repeat(p.value, p.bytes))})
	}
	return nil
}

// InjectFromWorkflow emits the same deterministic fields during workflow scheduling.
func (p *composedHeaderFixture) InjectFromWorkflow(_ workflow.Context, writer workflow.HeaderWriter) error {
	return p.Inject(context.Background(), writer)
}

// Extract verifies all configured providers still contribute their context values.
func (p *composedHeaderFixture) Extract(ctx context.Context, _ workflow.HeaderReader) (context.Context, error) {
	return context.WithValue(ctx, composedHeaderKey(p.value), p.value), nil
}

// ExtractToWorkflow retains every provider's value through the guarded chain.
func (p *composedHeaderFixture) ExtractToWorkflow(ctx workflow.Context, _ workflow.HeaderReader) (workflow.Context, error) {
	return workflow.WithValue(ctx, composedHeaderKey(p.value), p.value), nil
}

// composedHeaderTraffic observes actual SDK start construction without sending a network request.
type composedHeaderTraffic struct {
	start *workflowservice.StartWorkflowExecutionRequest
}

// CheckCallAllowed allows SDK capability fallback and captures the request after propagation preflight.
func (c *composedHeaderTraffic) CheckCallAllowed(_ context.Context, method string, request, _ interface{}) error {
	if strings.HasSuffix(method, "/GetSystemInfo") {
		return status.Error(codes.Unimplemented, "synthetic capability fallback")
	}
	if start, ok := request.(*workflowservice.StartWorkflowExecutionRequest); ok {
		c.start = start
	}
	return status.Error(codes.InvalidArgument, "synthetic local traffic capture")
}

// TestCombinedPropagationRejectsBeforeSDKAcceptance checks SDK starts and workflow commands against the same merged header budget.
func TestCombinedPropagationRejectsBeforeSDKAcceptance(t *testing.T) {
	for _, mode := range []string{"valid", "count", "bytes", "overlap"} {
		t.Run(mode, func(t *testing.T) {
			first := &composedHeaderFixture{prefix: "a", value: "a", count: 8, bytes: 1}
			second := &composedHeaderFixture{prefix: "b", value: "b", count: 8, bytes: 1}
			wantKeys := 16
			switch mode {
			case "count":
				first.count, second.count = 9, 9
			case "bytes":
				first.count, second.count = 5, 5
				first.bytes, second.bytes = 3500, 3500
			case "overlap":
				first.count, second.count, second.prefix, wantKeys = 9, 9, "a", 9
			}
			traffic := &composedHeaderTraffic{}
			binding, err := NewClientBinding(client.Options{TrafficController: traffic, ContextPropagators: []workflow.ContextPropagator{first, second}}, SerializationLimits{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = binding.Close(context.Background()) })
			if len(binding.options.ContextPropagators) != 1 || binding.options.ContextPropagators[0].(BoundedContextPropagator).MaxDecodedBytes() != 2048 {
				t.Fatal("configured propagators did not retain their combined decoding declaration")
			}
			_, startErr := binding.Client().ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: "fixture", TaskQueue: "fixture"}, "native")
			invalid := mode == "count" || mode == "bytes"
			if invalid {
				requirePropagationCategory(t, startErr, false)
				if traffic.start != nil {
					t.Fatal("invalid merged headers reached an SDK start request")
				}
			} else if traffic.start == nil || len(traffic.start.Header.Fields) != wantKeys {
				t.Fatal("valid merged headers never reached SDK request construction", startErr)
			}
			outer := binding.options.ContextPropagators[0]
			buffer := &headerBuffer{values: make(map[string]*common.Payload)}
			env := guardedWorkflowEnvironment(t)
			env.ExecuteWorkflow(func(ctx workflow.Context) error { return outer.InjectFromWorkflow(ctx, buffer) })
			if invalid {
				requirePropagationCategory(t, env.GetWorkflowError(), false)
				if len(buffer.values) != 0 {
					t.Fatal("failed aggregate preflight partially forwarded headers")
				}
				return
			}
			if err := env.GetWorkflowError(); err != nil || len(buffer.values) != wantKeys {
				t.Fatal("valid workflow header composition changed", len(buffer.values), err)
			}
			if mode == "overlap" && string(buffer.values["a0"].Data) != "b" {
				t.Fatal("SDK header overwrite order changed")
			}
			decoded, err := outer.Extract(t.Context(), buffer)
			if err != nil || decoded.Value(composedHeaderKey("a")) != "a" || decoded.Value(composedHeaderKey("b")) != "b" {
				t.Fatal("extraction lost configured provider context", err)
			}
			receiver := guardedWorkflowEnvironment(t)
			receiver.SetContextPropagators(binding.options.ContextPropagators)
			receiver.SetHeader(traffic.start.Header)
			receiver.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&resourceInterceptor{gate: newAdmission(), limits: DefaultSerializationLimits()}}})
			calls := 0
			receiver.ExecuteWorkflow(func(ctx workflow.Context) error {
				calls++
				if ctx.Value(composedHeaderKey("a")) != "a" || ctx.Value(composedHeaderKey("b")) != "b" {
					t.Fatal("workflow extraction lost provider context")
				}
				return nil
			})
			if err := receiver.GetWorkflowError(); err != nil || calls != 1 {
				t.Fatal("sender-produced header rejected by the same binding", calls, err)
			}
		})
	}
}

// TestCombinedPropagationFailureBoundaries preserves per-adapter recovery budgets and atomic output through composition.
func TestCombinedPropagationFailureBoundaries(t *testing.T) {
	for _, method := range []string{"client", "workflow"} {
		for _, mode := range []string{"error", "panic", "unavailable", "recover", "closed"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				first := &composedHeaderFixture{prefix: "first", value: "a", count: 1, bytes: 1}
				second := &propagationFixture{mode: mode}
				binding, err := NewClientBinding(client.Options{ContextPropagators: []workflow.ContextPropagator{first, second}}, SerializationLimits{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = binding.Close(context.Background()) })
				if mode == "closed" {
					if err := binding.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				outer := binding.options.ContextPropagators[0]
				output := &headerBuffer{values: make(map[string]*common.Payload)}
				if method == "client" {
					err = outer.Inject(t.Context(), output)
				} else {
					env := guardedWorkflowEnvironment(t)
					env.ExecuteWorkflow(func(ctx workflow.Context) error { return outer.InjectFromWorkflow(ctx, output) })
					err = env.GetWorkflowError()
				}
				wantCalls := 1
				switch mode {
				case "closed":
					wantCalls = 0
				case "unavailable", "recover":
					wantCalls = 3
				}
				if second.calls != wantCalls {
					t.Fatalf("adapter calls=%d want=%d", second.calls, wantCalls)
				}
				if mode == "recover" {
					if err != nil || len(output.values) != 2 || output.values["first0"] == nil || output.values["trace"] == nil || output.values["partial"] != nil {
						t.Fatal("composed recovery lost or leaked output", output, err)
					}
				} else {
					requirePropagationCategory(t, err, mode == "unavailable" || mode == "closed")
					if len(output.values) != 0 {
						t.Fatal("failed composition forwarded headers")
					}
				}
			})
		}
	}
}

// TestCombinedExtractionStopsAtRejection keeps later adapters from masking a rejected workflow context.
func TestCombinedExtractionStopsAtRejection(t *testing.T) {
	for _, method := range []string{"client", "workflow"} {
		for _, mode := range []string{"error", "panic", "unavailable", "nil", "closed"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				first, second := &propagationFixture{mode: mode}, &propagationFixture{}
				binding, err := NewClientBinding(client.Options{ContextPropagators: []workflow.ContextPropagator{first, second}}, SerializationLimits{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = binding.Close(context.Background()) })
				if mode == "closed" {
					if err := binding.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				outer := binding.options.ContextPropagators[0]
				input := &headerBuffer{values: map[string]*common.Payload{"trace": {Data: []byte("ok")}}}
				if method == "client" {
					_, err = outer.Extract(t.Context(), input)
					requirePropagationCategory(t, err, mode == "unavailable" || mode == "closed")
				} else {
					env := guardedWorkflowEnvironment(t)
					env.ExecuteWorkflow(func(ctx workflow.Context) error {
						extracted, err := outer.ExtractToWorkflow(ctx, input)
						if err != nil {
							return err
						}
						category := "invalid"
						if mode == "unavailable" || mode == "closed" {
							category = "unavailable"
						}
						if extracted.Value(headerRejectionKey{}) != category {
							t.Fatal("workflow rejection marker lost")
						}
						return nil
					})
					if err := env.GetWorkflowError(); err != nil {
						t.Fatal(err)
					}
				}
				wantCalls := 1
				if mode == "closed" {
					wantCalls = 0
				} else if mode == "unavailable" {
					wantCalls = 3
				}
				if first.calls != wantCalls || second.calls != 0 {
					t.Fatal("extraction continued after rejection", first.calls, second.calls)
				}
			})
		}
	}
}
