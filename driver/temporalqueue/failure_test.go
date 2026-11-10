package temporalqueue

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
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

// TestWrappedAdapterRejectionRemainsTerminal preserves module rejection across actual SDK decode wrapping forms.
func TestWrappedAdapterRejectionRemainsTerminal(t *testing.T) {
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	var value string
	rejection := dc.FromPayload(&common.Payload{Data: make([]byte, DefaultSerializationLimits().MaxPayloadBytes+1)}, &value)
	binding, err := newGuardedFailureConverter(nil, dc, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	worker := &guardedFailureConverter{inner: binding, dc: dc, gate: newAdmission(), protected: true}
	for _, guard := range []*guardedFailureConverter{binding, worker} {
		f := guard.ErrorToFailure(fmt.Errorf("unable to decode workflow input: %w", rejection))
		app := f.GetApplicationFailureInfo()
		if app == nil || !app.NonRetryable || app.Type != "goforj.invalid.v1" {
			t.Fatalf("SDK wrapping made module input retryable: %+v", f)
		}
		native := temporal.NewNonRetryableApplicationError("private native detail", "native-child", nil)
		f = guard.ErrorToFailure(fmt.Errorf("native application wrapper: %w", native))
		if app := f.GetApplicationFailureInfo(); app == nil || app.NonRetryable || app.Type == "goforj.invalid.v1" {
			t.Fatal("genuine native wrapper taxonomy changed")
		}
		if child := f.GetCause().GetApplicationFailureInfo(); child == nil || !child.NonRetryable || child.Type != "native-child" {
			t.Fatal("genuine native cause taxonomy changed")
		}
	}
}

// nativeMultiCycle models a native aggregate whose graph is outside SDK single-cause failure semantics.
type nativeMultiCycle struct{ visits int }

// Error supplies fixed native diagnostics without traversing the error graph.
func (*nativeMultiCycle) Error() string { return "synthetic native multi-error" }

// Unwrap exposes a cyclic multi-cause graph that module marker detection must not traverse.
func (e *nativeMultiCycle) Unwrap() []error { e.visits++; return []error{e} }

// TestAdapterMarkerSearchDoesNotTraverseNativeErrorGraphs preserves the SDK's bounded single-cause path.
func TestAdapterMarkerSearchDoesNotTraverseNativeErrorGraphs(t *testing.T) {
	native := &nativeMultiCycle{}
	if terminalAdapterCause(native) != native || native.visits != 0 {
		t.Fatal("marker search traversed a native multi-cause graph")
	}
	dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	guarded, err := newGuardedFailureConverter(nil, dc, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	failure := guarded.ErrorToFailure(native)
	if failure.GetApplicationFailureInfo() == nil || native.visits != 0 {
		t.Fatal("native aggregate was recursively inspected")
	}
}

// journalFailureConverter exposes authoritative serialization rejection before the SDK's default fallback.
type journalFailureConverter struct {
	converter.DataConverter
	failure error
	calls   int
}

// DecodingLimits keeps the test adapter within the real binding's declared bounds.
func (*journalFailureConverter) DecodingLimits() SerializationLimits {
	return DefaultSerializationLimits()
}

// MaxEncodedOverhead reserves ordinary JSON payload metadata only.
func (*journalFailureConverter) MaxEncodedOverhead() int { return 256 }

// ToPayload rejects only journal publication so workflow argument decoding retains its normal SDK behavior.
func (c *journalFailureConverter) ToPayload(value any) (*common.Payload, error) {
	if _, ok := value.(journalDelta); ok {
		c.calls++
		return nil, c.failure
	}
	return c.DataConverter.ToPayload(value)
}

// TestJournalPublicationPreservesAuthoritativeFailureClass uses actual SDK memo encoding to retain availability and terminal input categories.
func TestJournalPublicationPreservesAuthoritativeFailureClass(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable=%t", unavailable), func(t *testing.T) {
			rejection := errors.New("synthetic private codec failure")
			expectedCalls := 1
			expectedType := "goforj.invalid.v1"
			if unavailable {
				rejection = ErrAdapterUnavailable
				expectedCalls = 3
				expectedType = "goforj.adapter-unavailable.v1"
			}
			inner := &journalFailureConverter{DataConverter: converter.GetDefaultDataConverter(), failure: rejection}
			dc, err := newGuardedConverter(inner, DefaultSerializationLimits(), newAdmission())
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
			var observed error
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				observed = publishJournal(ctx, journalDelta{Version: 1, Sequence: 1})
				return observed
			})
			failure := fc.ErrorToFailure(observed)
			app := failure.GetApplicationFailureInfo()
			if observed == nil || inner.calls != expectedCalls || app == nil || app.Type != expectedType || app.NonRetryable == unavailable {
				t.Fatalf("SDK erased authoritative rejection: calls=%d observed=%v failure=%+v", inner.calls, observed, failure)
			}
			if unavailable && !adapterUnavailable(observed) {
				t.Fatal("key availability class was erased")
			}
		})
	}
}
