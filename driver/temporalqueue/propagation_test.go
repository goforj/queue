package temporalqueue

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// propagationValue identifies fixture context without relying on application string keys.
type propagationValue struct{}

// propagationFixture exposes adapter effects separately from the SDK forwarding boundary.
type propagationFixture struct {
	mode  string
	calls int
	order []string
}

// MaxDecodedBytes declares the fixture's bounded header decoding contract.
func (*propagationFixture) MaxDecodedBytes() int { return 32768 }

// inject models partial adapter output so retries must discard previous header attempts.
func (p *propagationFixture) inject(writer workflow.HeaderWriter) error {
	p.calls++
	switch p.mode {
	case "panic":
		panic("private propagation diagnostic")
	case "error":
		return errors.New("private propagation diagnostic")
	case "unavailable":
		return ErrAdapterUnavailable
	case "oversized":
		writer.Set("trace", &common.Payload{Data: make([]byte, 4097)})
		return nil
	case "recover":
		if p.calls < 3 {
			writer.Set("partial", &common.Payload{Data: []byte("discard")})
			return ErrAdapterUnavailable
		}
	}
	writer.Set("trace", &common.Payload{Data: []byte("trace-value")})
	return nil
}

// Inject models ordinary client propagation under the same bounded contract as workflow injection.
func (p *propagationFixture) Inject(_ context.Context, writer workflow.HeaderWriter) error {
	return p.inject(writer)
}

// InjectFromWorkflow avoids non-deterministic fixture state beyond per-environment call counting.
func (p *propagationFixture) InjectFromWorkflow(_ workflow.Context, writer workflow.HeaderWriter) error {
	return p.inject(writer)
}

// extract records stable iteration and attempts mutation to verify isolation from received headers.
func (p *propagationFixture) extract(reader workflow.HeaderReader) error {
	p.calls++
	switch p.mode {
	case "panic":
		panic("private propagation diagnostic")
	case "error":
		return errors.New("private propagation diagnostic")
	case "unavailable":
		return ErrAdapterUnavailable
	}
	return reader.ForEachKey(func(key string, value *common.Payload) error {
		p.order = append(p.order, key)
		if len(value.Data) > 0 {
			value.Data[0] = 'X'
		}
		return nil
	})
}

// Extract supplies a context value only after the fixture accepts the entire header set.
func (p *propagationFixture) Extract(ctx context.Context, reader workflow.HeaderReader) (context.Context, error) {
	if err := p.extract(reader); err != nil {
		return nil, err
	}
	if p.mode == "nil" {
		return nil, nil
	}
	return context.WithValue(ctx, propagationValue{}, "propagated"), nil
}

// ExtractToWorkflow uses SDK context values so rejection can be tested at actual workflow entry.
func (p *propagationFixture) ExtractToWorkflow(ctx workflow.Context, reader workflow.HeaderReader) (workflow.Context, error) {
	if err := p.extract(reader); err != nil {
		return nil, err
	}
	if p.mode == "nil" {
		return nil, nil
	}
	return workflow.WithValue(ctx, propagationValue{}, "propagated"), nil
}

// requirePropagationCategory checks safe retry semantics independently of private wrapper types.
func requirePropagationCategory(t *testing.T, err error, unavailable bool) {
	t.Helper()
	var application *temporal.ApplicationError
	if !errors.As(err, &application) || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe or unclassified propagation failure: %v", err)
	}
	want := "goforj.invalid.v1"
	if unavailable {
		want = "goforj.adapter-unavailable.v1"
	}
	if application.Type() != want || application.NonRetryable() == unavailable {
		t.Fatalf("category=%s terminal=%t; want %s", application.Type(), application.NonRetryable(), want)
	}
}

// TestPropagationRejectsHeadersBeforeAdapter checks every incoming size boundary before application decoding.
func TestPropagationRejectsHeadersBeforeAdapter(t *testing.T) {
	cases := map[string]map[string]*common.Payload{
		"nil payload":   {"trace": nil},
		"long key":      {strings.Repeat("k", 129): {Data: []byte("v")}},
		"large payload": {"trace": {Data: make([]byte, 4094)}},
	}
	for name, count := range map[string]int{"count": 17, "aggregate": 9} {
		values := make(map[string]*common.Payload)
		for i := 0; i < count; i++ {
			size := 1
			if name == "aggregate" {
				size = 4093
			}
			values[fmt.Sprint(i)] = &common.Payload{Data: make([]byte, size)}
		}
		cases[name] = values
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := &propagationFixture{}
			guard := &guardedPropagator{inner: fixture, gate: newAdmission()}
			if _, err := guard.Extract(t.Context(), &headerBuffer{values: values}); err == nil {
				t.Fatal("oversized headers accepted")
			} else {
				requirePropagationCategory(t, err, false)
			}
			if fixture.calls != 0 {
				t.Fatal("invalid headers reached adapter")
			}
		})
	}
	values := make(map[string]*common.Payload)
	for i := 0; i < 8; i++ {
		values[fmt.Sprint(i)] = &common.Payload{Data: make([]byte, 4093)}
	}
	if _, err := checkedHeaders(&headerBuffer{values: values}); err != nil {
		t.Fatalf("exact aggregate boundary rejected: %v", err)
	}
	small := make(map[string]*common.Payload)
	for i := 0; i < 16; i++ {
		small[fmt.Sprint(i)] = &common.Payload{}
	}
	small[strings.Repeat("k", 128)] = small["0"]
	delete(small, "0")
	if _, err := checkedHeaders(&headerBuffer{values: small}); err != nil {
		t.Fatalf("exact count/key boundary rejected: %v", err)
	}
}

// TestPropagationIsolationAndStableIteration protects retained SDK headers from adapter mutation and map-order nondeterminism.
func TestPropagationIsolationAndStableIteration(t *testing.T) {
	fixture := &propagationFixture{}
	guard := &guardedPropagator{inner: fixture, gate: newAdmission()}
	input := &headerBuffer{values: map[string]*common.Payload{"z": {Data: []byte("last")}, "a": {Data: []byte("first")}}}
	result, err := guard.Extract(t.Context(), input)
	if err != nil || result.Value(propagationValue{}) != "propagated" {
		t.Fatalf("context lost: %v", err)
	}
	if !reflect.DeepEqual(fixture.order, []string{"a", "z"}) || string(input.values["a"].Data) != "first" {
		t.Fatalf("order/isolation lost: %v %+v", fixture.order, input)
	}
	if guard.MaxDecodedBytes() != 32768 {
		t.Fatal("expansion contract lost")
	}
	if _, ok := input.Get("absent"); ok {
		t.Fatal("unexpected header")
	}
	stop := errors.New("stop iteration")
	calls := 0
	if err := input.ForEachKey(func(string, *common.Payload) error { calls++; return stop }); !errors.Is(err, stop) || calls != 1 {
		t.Fatal("iteration failure ignored")
	}
}

// TestPropagationOutgoingRetryAndRejection prevents partially generated headers from leaking across attempts or failures.
func TestPropagationOutgoingRetryAndRejection(t *testing.T) {
	for _, mode := range []string{"success", "recover", "error", "panic", "unavailable", "oversized", "closed"} {
		t.Run(mode, func(t *testing.T) {
			fixture := &propagationFixture{mode: mode}
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			guard := &guardedPropagator{inner: fixture, gate: gate}
			output := &headerBuffer{values: make(map[string]*common.Payload)}
			err := guard.Inject(t.Context(), output)
			if mode == "success" || mode == "recover" {
				if err != nil || len(output.values) != 1 || string(output.values["trace"].Data) != "trace-value" {
					t.Fatalf("bad outgoing headers: %+v %v", output, err)
				}
				if mode == "recover" && fixture.calls != 3 {
					t.Fatal("wrong availability retry budget")
				}
			} else {
				requirePropagationCategory(t, err, mode == "unavailable" || mode == "closed")
				if len(output.values) != 0 {
					t.Fatal("failed adapter forwarded partial headers")
				}
				want := 1
				if mode == "unavailable" {
					want = 3
				}
				if mode == "closed" {
					want = 0
				}
				if fixture.calls != want {
					t.Fatalf("adapter calls=%d want=%d", fixture.calls, want)
				}
			}
			if err := gate.wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, mode := range []string{"nil", "error", "panic", "unavailable", "closed"} {
		t.Run("extract/"+mode, func(t *testing.T) {
			fixture := &propagationFixture{mode: mode}
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			guard := &guardedPropagator{inner: fixture, gate: gate}
			_, err := guard.Extract(t.Context(), &headerBuffer{values: map[string]*common.Payload{}})
			requirePropagationCategory(t, err, mode == "unavailable" || mode == "closed")
		})
	}
}

// TestWorkflowPropagationRejectsBeforeBody exercises real SDK extraction and the module's deterministic workflow-entry guard.
func TestWorkflowPropagationRejectsBeforeBody(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "nil", "unavailable", "closed", "oversized input"} {
		t.Run(mode, func(t *testing.T) {
			env := guardedWorkflowEnvironment(t)
			fixture := &propagationFixture{mode: mode}
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			guard := &guardedPropagator{inner: fixture, gate: gate}
			env.SetContextPropagators([]workflow.ContextPropagator{guard})
			payload := []byte("input")
			if mode == "oversized input" {
				payload = make([]byte, 4094)
			}
			env.SetHeader(&common.Header{Fields: map[string]*common.Payload{"trace": {Data: payload}}})
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&resourceInterceptor{gate: newAdmission(), limits: DefaultSerializationLimits()}}})
			calls := 0
			env.ExecuteWorkflow(func(ctx workflow.Context) (string, error) {
				calls++
				output := &headerBuffer{values: make(map[string]*common.Payload)}
				if err := guard.InjectFromWorkflow(ctx, output); err != nil {
					return "", err
				}
				return ctx.Value(propagationValue{}).(string) + ":" + string(output.values["trace"].Data), nil
			})
			if mode == "success" {
				var result string
				if err := env.GetWorkflowResult(&result); err != nil || result != "propagated:trace-value" || calls != 1 {
					t.Fatalf("workflow roundtrip: result=%s err=%v calls=%d", result, err, calls)
				}
			} else {
				requirePropagationCategory(t, env.GetWorkflowError(), mode == "unavailable" || mode == "closed")
				if calls != 0 {
					t.Fatal("rejected propagation entered application workflow")
				}
			}
		})
	}
}

// TestWorkflowInjectionFailureDoesNotForwardHeaders covers workflow-only output validation without conflating it with extraction.
func TestWorkflowInjectionFailureDoesNotForwardHeaders(t *testing.T) {
	for _, mode := range []string{"recover", "error", "panic", "unavailable", "oversized", "closed"} {
		t.Run(mode, func(t *testing.T) {
			env := guardedWorkflowEnvironment(t)
			fixture := &propagationFixture{mode: mode}
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			guard := &guardedPropagator{inner: fixture, gate: gate}
			output := &headerBuffer{values: make(map[string]*common.Payload)}
			env.ExecuteWorkflow(func(ctx workflow.Context) error { return guard.InjectFromWorkflow(ctx, output) })
			if mode == "recover" {
				if env.GetWorkflowError() != nil || len(output.values) != 1 || fixture.calls != 3 {
					t.Fatalf("workflow recovery failed: %+v %v", output, env.GetWorkflowError())
				}
			} else {
				requirePropagationCategory(t, env.GetWorkflowError(), mode == "unavailable" || mode == "closed")
				if len(output.values) != 0 {
					t.Fatal("workflow failure leaked headers")
				}
			}
		})
	}
}

// guardedWorkflowEnvironment retains the production converter and failure boundaries during SDK workflow execution.
func guardedWorkflowEnvironment(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	fc, err := newGuardedFailureConverter(nil, dc, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetDataConverter(dc)
	env.SetFailureConverter(fc)
	return env
}
