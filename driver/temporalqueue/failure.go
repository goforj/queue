package temporalqueue

import (
	"errors"
	"go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"
	"reflect"
)

// ProtectedFailureConverter declares bounded decoding and protection of native error details.
// Implementations must encrypt detail payloads and common attributes, enforce a
// maximum of 16 cause levels and 256 KiB encoded failures, and fail closed when
// common attributes cannot be decrypted. Serialization-context copies must retain
// these guarantees. Error type names remain visible retry-policy metadata.
type ProtectedFailureConverter interface {
	converter.FailureConverter
	// ProtectsFailureDetails confirms the adapter's authenticated encryption policy.
	ProtectsFailureDetails() bool
}

// guardedFailureConverter preserves SDK error taxonomy while fencing adapter callbacks.
type guardedFailureConverter struct {
	inner        converter.FailureConverter
	dc           converter.DataConverter
	gate         *admission
	protected    bool
	workflowTask bool
	// workflowContext retains workflow execution ownership across SDK activity-context copies.
	workflowContext bool
}

// newGuardedFailureConverter requires an explicit protection contract for custom failure adapters.
func newGuardedFailureConverter(inner converter.FailureConverter, dc converter.DataConverter, gate *admission) (*guardedFailureConverter, error) {
	protected := false
	if inner != nil {
		adapter, ok := inner.(ProtectedFailureConverter)
		if !ok || !adapter.ProtectsFailureDetails() {
			return nil, errors.New("custom Temporal failure converter must protect and bound native details")
		}
		protected = true
	} else {
		inner = temporal.NewDefaultFailureConverter(temporal.DefaultFailureConverterOptions{DataConverter: dc})
	}
	return &guardedFailureConverter{inner: inner, dc: dc, gate: gate, protected: protected}, nil
}

// protocolFailure returns a stable terminal classification for malformed adapter input.
func protocolFailure() *failure.Failure {
	return &failure.Failure{Message: "Temporal failure adapter rejected input", FailureInfo: &failure.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failure.ApplicationFailureInfo{Type: "goforj.protocol.v1", NonRetryable: true}}}
}

// validateFailure bounds recursive structure before protobuf cloning or SDK recursion.
func validateFailure(f *failure.Failure) bool {
	seen := make(map[*failure.Failure]bool)
	for current, depth := f, 0; current != nil; current, depth = current.Cause, depth+1 {
		if depth >= 16 || seen[current] {
			return false
		}
		seen[current] = true
	}
	return f == nil || proto.Size(f) <= 256<<10
}

// redactFailure removes diagnostic content without collapsing native SDK error categories.
func redactFailure(f *failure.Failure) *failure.Failure {
	if f == nil {
		return nil
	}
	if !validateFailure(f) {
		return protocolFailure()
	}
	cloned := proto.Clone(f).(*failure.Failure)
	for current := cloned; current != nil; current = current.Cause {
		current.Message = "application execution failed"
		current.StackTrace = ""
		current.EncodedAttributes = nil
		if info := current.GetApplicationFailureInfo(); info != nil {
			info.Details = nil
			if len(info.Type) > 256 {
				info.Type = "goforj.execution.v1"
			}
		}
		if info := current.GetCanceledFailureInfo(); info != nil {
			info.Details = nil
		}
		if info := current.GetTimeoutFailureInfo(); info != nil {
			info.LastHeartbeatDetails = nil
		}
		if info := current.GetResetWorkflowFailureInfo(); info != nil {
			info.LastHeartbeatDetails = nil
		}
	}
	return cloned
}

// ErrorToFailure applies protection before failures cross the SDK persistence boundary.
func (c *guardedFailureConverter) ErrorToFailure(err error) (result *failure.Failure) {
	if err == nil {
		return nil
	}
	result = protocolFailure()
	defer func() {
		if recover() != nil {
			result = protocolFailure()
		}
	}()
	release, gateErr := c.gate.enter()
	if gateErr != nil {
		return unavailableFailure()
	}
	defer release()
	// The SDK recursively unwraps errors; reject a cycle or excessive cause depth first.
	seen := make(map[error]bool)
	for e, depth := err, 0; e != nil; e, depth = errors.Unwrap(e), depth+1 {
		if depth >= 16 {
			return result
		}
		// Some error implementations contain slices and cannot be map keys.
		if reflect.TypeOf(e).Comparable() {
			if seen[e] {
				return result
			}
			seen[e] = true
		}
	}
	encoded := c.inner.ErrorToFailure(terminalAdapterCause(err))
	if !validateFailure(encoded) {
		return result
	}
	if !c.protected {
		return redactFailure(encoded)
	}
	return encoded
}

// FailureToError retains native wrappers, timeout types and retry metadata.
func (c *guardedFailureConverter) FailureToError(f *failure.Failure) (result error) {
	if f == nil {
		return nil
	}
	result = temporal.NewNonRetryableApplicationError("Temporal failure adapter rejected input", "goforj.protocol.v1", nil)
	defer func() {
		if recover() != nil {
			result = temporal.NewNonRetryableApplicationError("Temporal failure adapter rejected input", "goforj.protocol.v1", nil)
		}
	}()
	if !validateFailure(f) {
		return result
	}
	release, err := c.gate.enter()
	if err != nil {
		return unavailableAdapterError()
	}
	defer release()
	if !c.protected {
		f = redactFailure(f)
	}
	return c.inner.FailureToError(f)
}

// WithSerializationContext binds protected detail decoding to authoritative workflow identity.
func (c *guardedFailureConverter) WithSerializationContext(ctx converter.SerializationContext) (result converter.FailureConverter) {
	_, workflowScope := ctx.(converter.WorkflowSerializationContext)
	workflowContext := c.workflowContext || workflowScope
	defer func() {
		if recover() != nil {
			result = rejectedFailureConverter{}
		}
		if rejected, ok := result.(rejectedFailureConverter); ok && c.workflowTask && workflowContext {
			if rejected.unavailable {
				panic(unavailableAdapterError())
			}
			panic(invalidAdapterError())
		}
	}()
	dc := converter.WithDataConverterSerializationContext(c.dc, ctx)
	if rejected, ok := dc.(rejectedConverter); ok {
		return rejectedFailureConverter{unavailable: adapterUnavailable(rejected.err)}
	}
	inner := c.inner
	if !c.protected {
		inner = temporal.NewDefaultFailureConverter(temporal.DefaultFailureConverterOptions{DataConverter: dc})
	} else {
		release, err := c.gate.enter()
		if err != nil {
			return rejectedFailureConverter{unavailable: true}
		}
		defer release()
		if contextual, ok := inner.(converter.FailureConverterWithSerializationContext); ok {
			inner = contextual.WithSerializationContext(ctx)
		}
		if inner == nil {
			return rejectedFailureConverter{}
		}
		if rejected, ok := inner.(rejectedFailureConverter); ok {
			return rejected
		}
	}
	return &guardedFailureConverter{inner: inner, dc: dc, gate: c.gate, protected: c.protected, workflowTask: c.workflowTask, workflowContext: workflowContext}
}

// rejectedFailureConverter fences late calls without invoking application adapters.
type rejectedFailureConverter struct{ unavailable bool }

// ErrorToFailure returns a fixed terminal adapter rejection.
func (c rejectedFailureConverter) ErrorToFailure(error) *failure.Failure {
	if c.unavailable {
		return unavailableFailure()
	}
	return protocolFailure()
}

// FailureToError returns a fixed terminal adapter rejection.
func (c rejectedFailureConverter) FailureToError(*failure.Failure) error {
	if c.unavailable {
		return unavailableAdapterError()
	}
	return temporal.NewNonRetryableApplicationError("Temporal failure adapter rejected input", "goforj.protocol.v1", nil)
}

// unavailableFailure keeps late adapter fencing retryable after the SDK worker grace period expires.
func unavailableFailure() *failure.Failure {
	return &failure.Failure{Message: "Temporal adapter temporarily unavailable", FailureInfo: &failure.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failure.ApplicationFailureInfo{Type: "goforj.adapter-unavailable.v1", NonRetryable: false}}}
}
