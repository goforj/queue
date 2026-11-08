package temporalqueue

import (
	"errors"
	"strings"
	"testing"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestFailureConverterPreservesNativeTaxonomy verifies retry metadata survives diagnostic redaction.
func TestFailureConverterPreservesNativeTaxonomy(t *testing.T) {
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	guarded, err := newGuardedFailureConverter(nil, dc, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	native := temporal.NewTimeoutError(enums.TIMEOUT_TYPE_HEARTBEAT, temporal.NewNonRetryableApplicationError("private application message", "declared.public.type", nil, "private application detail"), "private heartbeat detail")
	encoded := guarded.ErrorToFailure(native)
	raw, err := protojson.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatalf("private diagnostics persisted: %s", raw)
	}
	decoded := guarded.FailureToError(encoded)
	var timeout *temporal.TimeoutError
	if !errors.As(decoded, &timeout) || timeout.TimeoutType() != enums.TIMEOUT_TYPE_HEARTBEAT {
		t.Fatalf("native timeout taxonomy lost: %T %v", decoded, decoded)
	}
	var application *temporal.ApplicationError
	if !errors.As(decoded, &application) || !application.NonRetryable() || application.Type() != "declared.public.type" {
		t.Fatalf("native application retry metadata lost: %v", decoded)
	}
}

// TestFailureConverterRejectsRecursiveAndOversizedInput bounds malformed failures before SDK cloning.
func TestFailureConverterRejectsRecursiveAndOversizedInput(t *testing.T) {
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	guarded, err := newGuardedFailureConverter(nil, dc, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	cycle := &failure.Failure{}
	cycle.Cause = cycle
	deep := &failure.Failure{}
	current := deep
	for range 17 {
		current.Cause = &failure.Failure{}
		current = current.Cause
	}
	for name, input := range map[string]*failure.Failure{"cycle": cycle, "depth": deep, "bytes": {Message: strings.Repeat("x", 256<<10)}} {
		t.Run(name, func(t *testing.T) {
			err := guarded.FailureToError(input)
			var application *temporal.ApplicationError
			if !errors.As(err, &application) || !application.NonRetryable() || application.Type() != "goforj.protocol.v1" {
				t.Fatalf("malformed failure accepted: %v", err)
			}
		})
	}
}

// TestLateFailureAdaptersRemainRetryable keeps shutdown availability distinct from invalid application input.
func TestLateFailureAdaptersRemainRetryable(t *testing.T) {
	gate := newAdmission()
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), gate)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := newGuardedConverter(dc, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	failures, err := newGuardedFailureConverter(nil, dc, gate)
	if err != nil {
		t.Fatal(err)
	}
	gate.close()
	if _, err := outer.ToPayload("late typed result"); !adapterUnavailable(err) {
		t.Fatalf("late conversion became terminal: %v", err)
	}
	encoded := failures.ErrorToFailure(errors.New("late retryable failure"))
	info := encoded.GetApplicationFailureInfo()
	if info == nil || info.NonRetryable || info.Type != "goforj.adapter-unavailable.v1" {
		t.Fatalf("late failure became terminal %+v", encoded)
	}
	var application *temporal.ApplicationError
	if err := failures.FailureToError(encoded); !errors.As(err, &application) || application.NonRetryable() || application.Type() != "goforj.adapter-unavailable.v1" {
		t.Fatalf("late decode category %v", err)
	}
	protected := &guardedFailureConverter{gate: gate, dc: dc, protected: true}
	contextual := protected.WithSerializationContext(converter.WorkflowSerializationContext{Namespace: "default", WorkflowID: "workflow"})
	if info := contextual.ErrorToFailure(errors.New("late")).GetApplicationFailureInfo(); info == nil || info.NonRetryable {
		t.Fatal("contextual late adapter became terminal")
	}
}
