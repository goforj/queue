package temporalqueue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"
)

// contextualConverterFixture models approved adapter copies independently of outer SDK lifetime fencing.
type contextualConverterFixture struct {
	converter.DataConverter
	mode              string
	calls             int
	overhead          int
	limits            SerializationLimits
	seenContext       context.Context
	seenWorkflow      workflow.Context
	seenSerialization converter.SerializationContext
}

// DecodingLimits allows invalid declarations to be rejected before any conversion work.
func (c *contextualConverterFixture) DecodingLimits() SerializationLimits { return c.limits }

// MaxEncodedOverhead exposes the producer reservation contract during configuration checks.
func (c *contextualConverterFixture) MaxEncodedOverhead() int { return c.overhead }

// copy records context-factory entry and controls invalid-copy behavior.
func (c *contextualConverterFixture) copy() converter.DataConverter {
	c.calls++
	if c.mode == "panic" {
		panic("private contextual adapter diagnostic")
	}
	if c.mode == "nil" {
		return nil
	}
	return c
}

// WithSerializationContext retains authoritative SDK identity through approved fixture copies.
func (c *contextualConverterFixture) WithSerializationContext(ctx converter.SerializationContext) converter.DataConverter {
	c.seenSerialization = ctx
	return c.copy()
}

// WithContext retains the SDK's legacy activity-side contextual conversion path.
func (c *contextualConverterFixture) WithContext(ctx context.Context) converter.DataConverter {
	c.seenContext = ctx
	return c.copy()
}

// WithWorkflowContext retains the SDK's legacy replay-side contextual conversion path.
func (c *contextualConverterFixture) WithWorkflowContext(ctx workflow.Context) converter.DataConverter {
	c.seenWorkflow = ctx
	return c.copy()
}

// TestContextualConverterCopiesFailClosed checks every conversion entry point after context construction fails.
func TestContextualConverterCopiesFailClosed(t *testing.T) {
	for _, path := range []string{"serialization", "activity context", "workflow context"} {
		t.Run(path, func(t *testing.T) {
			for _, mode := range []string{"success", "panic", "nil", "closed"} {
				t.Run(mode, func(t *testing.T) {
					fixture := &contextualConverterFixture{DataConverter: converter.GetDefaultDataConverter(), mode: mode, overhead: 256, limits: DefaultSerializationLimits()}
					gate := newAdmission()
					guard, err := newGuardedConverter(fixture, DefaultSerializationLimits(), gate)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "closed" {
						gate.close()
					}
					var copy converter.DataConverter
					var expectedWorkflow workflow.Context
					expectedContext := context.WithValue(t.Context(), propagationValue{}, "context identity")
					expectedSerialization := converter.WorkflowSerializationContext{Namespace: "default", WorkflowID: "original"}
					switch path {
					case "serialization":
						copy = guard.WithSerializationContext(expectedSerialization)
					case "activity context":
						copy = guard.WithContext(expectedContext)
					case "workflow context":
						env := guardedWorkflowEnvironment(t)
						env.ExecuteWorkflow(func(ctx workflow.Context) error {
							expectedWorkflow = ctx
							copy = guard.WithWorkflowContext(ctx)
							return nil
						})
						if err := env.GetWorkflowError(); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "success" {
						if (path == "serialization" && fixture.seenSerialization != expectedSerialization) || (path == "activity context" && fixture.seenContext != expectedContext) || (path == "workflow context" && (expectedWorkflow == nil || fixture.seenWorkflow != expectedWorkflow)) {
							t.Fatal("converter copy lost authoritative context identity")
						}
						payload, err := copy.ToPayload("value")
						if err != nil {
							t.Fatal(err)
						}
						var value string
						if err := copy.FromPayload(payload, &value); err != nil || value != "value" {
							t.Fatal("contextual conversion changed", err)
						}
						return
					}
					payload := &common.Payload{Data: []byte("private payload")}
					if _, err := copy.ToPayload("value"); err == nil {
						t.Fatal("failed copy encoded payload")
					}
					if err := copy.FromPayload(payload, new(string)); err == nil {
						t.Fatal("failed copy decoded payload")
					}
					if _, err := copy.ToPayloads("value"); err == nil {
						t.Fatal("failed copy encoded arguments")
					}
					if err := copy.FromPayloads(&common.Payloads{Payloads: []*common.Payload{payload}}, new(string)); err == nil {
						t.Fatal("failed copy decoded arguments")
					}
					if copy.ToString(payload) != "<redacted>" || copy.ToStrings(&common.Payloads{Payloads: []*common.Payload{payload}})[0] != "<redacted>" {
						t.Fatal("failed-copy diagnostics exposed payload")
					}
					if mode == "closed" && fixture.calls != 0 {
						t.Fatal("late context factory touched disposed adapter")
					}
				})
			}
		})
	}
	for _, mode := range []string{"bad overhead", "negative overhead", "bad decoder limits"} {
		t.Run(mode, func(t *testing.T) {
			fixture := &contextualConverterFixture{DataConverter: converter.GetDefaultDataConverter(), overhead: 256, limits: DefaultSerializationLimits()}
			if mode == "bad overhead" {
				fixture.overhead = 4097
			}
			if mode == "negative overhead" {
				fixture.overhead = -1
			}
			if mode == "bad decoder limits" {
				fixture.limits.MaxPayloads = 0
			}
			if _, err := newGuardedConverter(fixture, DefaultSerializationLimits(), newAdmission()); err == nil {
				t.Fatal("unbounded adapter declaration accepted")
			}
		})
	}
}

// TestSerializationArgumentAndAggregateBounds guards all arguments before decoding and caps diagnostic work independently.
func TestSerializationArgumentAndAggregateBounds(t *testing.T) {
	limits := SerializationLimits{MaxPayloads: 2, MaxPayloadBytes: 1024, MaxTotalBytes: 1024}
	guard, err := newGuardedConverter(nil, limits, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.ToPayloads(1, 2, 3); err == nil {
		t.Fatal("excessive outgoing argument count accepted")
	}
	if _, err := guard.ToPayloads(strings.Repeat("x", 800), strings.Repeat("y", 800)); err == nil {
		t.Fatal("aggregate output byte budget ignored")
	}
	validArguments, err := converter.GetDefaultDataConverter().ToPayloads(strings.Repeat("x", 800), strings.Repeat("y", 800))
	if err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err := converter.GetDefaultDataConverter().FromPayloads(validArguments, &first, &second); err != nil || len(first) != 800 || len(second) != 800 {
		t.Fatal("aggregate fixture is not independently decodable", err)
	}
	if err := guard.FromPayloads(validArguments, new(string), new(string)); err == nil {
		t.Fatal("aggregate input byte budget ignored")
	}
	if guard.ToStrings(nil) != nil || len(guard.ToStrings(&common.Payloads{Payloads: make([]*common.Payload, 100)})) != 2 {
		t.Fatal("diagnostic work not capped")
	}
	if err := guard.FromPayload(&common.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("invalid JSON")}, new(string)); err == nil {
		t.Fatal("invalid JSON decoded successfully")
	}
	gate := newAdmission()
	closed, err := newGuardedConverter(nil, limits, gate)
	if err != nil {
		t.Fatal(err)
	}
	gate.close()
	if _, err := closed.ToPayloads("value"); !errors.Is(err, errAdmissionClosed) {
		t.Fatal("late argument encoding lost availability", err)
	}
	if err := closed.FromPayloads(nil); !errors.Is(err, errAdmissionClosed) {
		t.Fatal("late argument decoding lost availability", err)
	}
}
