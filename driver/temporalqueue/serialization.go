package temporalqueue

import (
	"context"
	"errors"
	"go.temporal.io/sdk/workflow"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

// SerializationLimits bound framework payloads before application conversion.
// Custom adapters must also bound expansion and work inside their own decoding.
type SerializationLimits struct {
	MaxPayloads     int
	MaxPayloadBytes int
	MaxTotalBytes   int
}

// DefaultSerializationLimits returns conservative limits below service message ceilings.
func DefaultSerializationLimits() SerializationLimits {
	return SerializationLimits{MaxPayloads: 16, MaxPayloadBytes: 256 << 10, MaxTotalBytes: 1 << 20}
}

// validate rejects unlimited or internally contradictory decoding budgets.
func (l SerializationLimits) validate() error {
	if l.MaxPayloads < 1 || l.MaxPayloads > 64 || l.MaxPayloadBytes < 1024 || l.MaxTotalBytes < l.MaxPayloadBytes || l.MaxTotalBytes > 2<<20 {
		return errors.New("invalid Temporal serialization limits")
	}
	return nil
}

// BoundedDataConverter declares the expansion, panic and work contract of a
// custom converter. A declaration is required in addition to outer byte guards.
// Implementations must honor their limits in context-specific converter copies.
type BoundedDataConverter interface {
	converter.DataConverter
	// DecodingLimits declares limits enforced inside custom decoding/expansion.
	DecodingLimits() SerializationLimits
	// MaxEncodedOverhead bounds bytes added beyond JSON encoding, including protection metadata.
	MaxEncodedOverhead() int
}

// guardedConverter keeps decoding, adapter admission and diagnostic redaction together.
type guardedConverter struct {
	inner    converter.DataConverter
	limits   SerializationLimits
	overhead int
	gate     *admission
}

// newGuardedConverter accepts default conversion or an explicitly bounded adapter.
func newGuardedConverter(inner converter.DataConverter, limits SerializationLimits, gate *admission) (*guardedConverter, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	overhead := 256
	if inner == nil {
		inner = converter.GetDefaultDataConverter()
	} else {
		bounded, ok := inner.(BoundedDataConverter)
		if !ok {
			return nil, errors.New("custom Temporal data converter must declare bounded decoding")
		}
		overhead = bounded.MaxEncodedOverhead()
		if overhead < 0 || overhead > 4096 {
			return nil, errors.New("custom Temporal encoding overhead must be bounded by 4096 bytes")
		}
		declared := bounded.DecodingLimits()
		if err := declared.validate(); err != nil {
			return nil, err
		}
		if declared.MaxPayloads > limits.MaxPayloads || declared.MaxPayloadBytes > limits.MaxPayloadBytes || declared.MaxTotalBytes > limits.MaxTotalBytes {
			return nil, errors.New("custom converter exceeds resource decoding limits")
		}
	}
	return &guardedConverter{inner: inner, limits: limits, overhead: overhead, gate: gate}, nil
}

// check rejects excessive encoded input before application decoding is entered.
func (c *guardedConverter) check(payloads []*common.Payload) error {
	if len(payloads) > c.limits.MaxPayloads {
		return invalidAdapterError()
	}
	total := 0
	for _, payload := range payloads {
		if payload == nil {
			return invalidAdapterError()
		}
		size := proto.Size(payload)
		if size > c.limits.MaxPayloadBytes || size > c.limits.MaxTotalBytes-total {
			return invalidAdapterError()
		}
		total += size
	}
	return nil
}

// conversionFailure sanitizes adapter panics without changing workflow panic policy.
func conversionFailure(err *error) {
	if recover() != nil {
		*err = invalidAdapterError()
	}
}

// ToPayload protects callback lifetime and enforces encoded-output bounds.
func (c *guardedConverter) ToPayload(value any) (payload *common.Payload, err error) {
	if journal, ok := value.(journalValue); ok {
		value = journal.Delta
	}
	defer conversionFailure(&err)
	release, err := c.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	payload, err = convertAdapter(func() (*common.Payload, error) { return c.inner.ToPayload(value) })
	if err != nil {
		return nil, err
	}
	if err = c.check([]*common.Payload{payload}); err != nil {
		return nil, err
	}
	return payload, nil
}

// FromPayload rejects oversized input before calling any custom decoder.
func (c *guardedConverter) FromPayload(payload *common.Payload, value any) (err error) {
	defer conversionFailure(&err)
	if err = c.check([]*common.Payload{payload}); err != nil {
		return err
	}
	release, err := c.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	_, err = convertAdapter(func() (struct{}, error) { return struct{}{}, c.inner.FromPayload(payload, value) })
	if err != nil {
		return err
	}
	return nil
}

// ToPayloads applies one aggregate bound rather than independent per-argument limits.
func (c *guardedConverter) ToPayloads(values ...any) (payloads *common.Payloads, err error) {
	defer conversionFailure(&err)
	if len(values) > c.limits.MaxPayloads {
		return nil, errors.New("Temporal argument count exceeds limit")
	}
	release, err := c.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	payloads, err = convertAdapter(func() (*common.Payloads, error) { return c.inner.ToPayloads(values...) })
	if err != nil {
		return nil, err
	}
	if payloads == nil {
		return nil, nil
	}
	if err = c.check(payloads.Payloads); err != nil {
		return nil, err
	}
	return payloads, nil
}

// FromPayloads validates all arguments before entering user-controlled decoding.
func (c *guardedConverter) FromPayloads(payloads *common.Payloads, values ...any) (err error) {
	defer conversionFailure(&err)
	if payloads != nil {
		if err = c.check(payloads.Payloads); err != nil {
			return err
		}
	}
	release, err := c.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	_, err = convertAdapter(func() (struct{}, error) { return struct{}{}, c.inner.FromPayloads(payloads, values...) })
	if err != nil {
		return err
	}
	return nil
}

// ToString keeps SDK diagnostics from exposing application payloads.
func (c *guardedConverter) ToString(*common.Payload) string { return "<redacted>" }

// ToStrings keeps payload-list diagnostics independent of decoding dependencies.
func (c *guardedConverter) ToStrings(payloads *common.Payloads) []string {
	if payloads == nil {
		return nil
	}
	count := len(payloads.Payloads)
	if count > c.limits.MaxPayloads {
		count = c.limits.MaxPayloads
	}
	result := make([]string, count)
	for i := range result {
		result[i] = "<redacted>"
	}
	return result
}

// WithSerializationContext preserves the authoritative SDK context during history reads.
func (c *guardedConverter) WithSerializationContext(ctx converter.SerializationContext) (result converter.DataConverter) {
	defer func() {
		if recover() != nil {
			result = rejectedConverter{err: errors.New("Temporal serialization context rejected")}
		}
	}()
	inner := c.inner
	if contextual, ok := inner.(converter.DataConverterWithSerializationContext); ok {
		// The adapter entry itself can access application dependencies.
		release, err := c.gate.enter()
		if err != nil {
			return rejectedConverter{err: err}
		}
		defer release()
		inner = contextual.WithSerializationContext(ctx)
		if inner == nil {
			return rejectedConverter{err: invalidAdapterError()}
		}
	}
	return &guardedConverter{inner: inner, limits: c.limits, overhead: c.overhead, gate: c.gate}
}

// rejectedConverter returns a stable shutdown classification without calling user code.
type rejectedConverter struct{ err error }

// ToPayload prevents late SDK encoding from reopening admission.
func (c rejectedConverter) ToPayload(any) (*common.Payload, error) { return nil, c.err }

// FromPayload prevents late SDK decoding from reopening admission.
func (c rejectedConverter) FromPayload(*common.Payload, any) error { return c.err }

// ToPayloads prevents late SDK encoding from reopening admission.
func (c rejectedConverter) ToPayloads(...any) (*common.Payloads, error) { return nil, c.err }

// FromPayloads prevents late SDK decoding from reopening admission.
func (c rejectedConverter) FromPayloads(*common.Payloads, ...any) error { return c.err }

// ToString never includes rejected input in diagnostics.
func (c rejectedConverter) ToString(*common.Payload) string { return "<redacted>" }

// ToStrings never includes rejected input in diagnostics.
func (c rejectedConverter) ToStrings(*common.Payloads) []string { return []string{"<redacted>"} }

// DecodingLimits retains the bound when a resource wraps an authoritative binding converter.
func (c *guardedConverter) DecodingLimits() SerializationLimits { return c.limits }

// WithContext preserves the SDK's legacy context-aware converter contract under the same bounds.
func (c *guardedConverter) WithContext(ctx context.Context) (result converter.DataConverter) {
	result = c
	defer func() {
		if recover() != nil {
			result = rejectedConverter{err: invalidAdapterError()}
		}
	}()
	if inner, ok := c.inner.(interface {
		WithContext(context.Context) converter.DataConverter
	}); ok {
		release, err := c.gate.enter()
		if err != nil {
			return rejectedConverter{err: err}
		}
		defer release()
		bound := inner.WithContext(ctx)
		if bound == nil {
			return rejectedConverter{err: invalidAdapterError()}
		}
		return &guardedConverter{inner: bound, limits: c.limits, overhead: c.overhead, gate: c.gate}
	}
	return result
}

// WithWorkflowContext preserves replay-safe contextual converter copies without changing workflow panic policy.
func (c *guardedConverter) WithWorkflowContext(ctx workflow.Context) (result converter.DataConverter) {
	result = c
	defer func() {
		if recover() != nil {
			result = rejectedConverter{err: invalidAdapterError()}
		}
	}()
	if inner, ok := c.inner.(interface {
		WithWorkflowContext(workflow.Context) converter.DataConverter
	}); ok {
		release, err := c.gate.enter()
		if err != nil {
			return rejectedConverter{err: err}
		}
		defer release()
		bound := inner.WithWorkflowContext(ctx)
		if bound == nil {
			return rejectedConverter{err: invalidAdapterError()}
		}
		return &guardedConverter{inner: bound, limits: c.limits, overhead: c.overhead, gate: c.gate}
	}
	return result
}

// MaxEncodedOverhead retains the approved encoding expansion bound across resource/context wrappers.
func (c *guardedConverter) MaxEncodedOverhead() int { return c.overhead }
