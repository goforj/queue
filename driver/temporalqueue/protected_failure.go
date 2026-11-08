package temporalqueue

import (
	"errors"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"
)

// ProtectedDataConverter declares authenticated payload protection in addition to bounded decoding.
// ValidateProtectedPayload must reject plaintext, unknown key IDs, and malformed
// protection metadata without unbounded decoding or external work. Contextual
// copies must preserve this policy and reject failed authentication.
type ProtectedDataConverter interface {
	BoundedDataConverter
	// ValidateProtectedPayload checks the adapter's mandatory protection envelope.
	ValidateProtectedPayload(*common.Payload) error
}

// NewProtectedFailureConverter preserves native failure details through an approved protected data converter.
// Error type names remain visible because Temporal uses them for retry matching.
func NewProtectedFailureConverter(dc ProtectedDataConverter) (result ProtectedFailureConverter, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = errors.New("protected failure converter policy rejected")
		}
	}()
	if dc == nil || dc.MaxEncodedOverhead() < 0 || dc.MaxEncodedOverhead() > 4096 {
		return nil, errors.New("invalid protected failure converter")
	}
	if err := dc.DecodingLimits().validate(); err != nil {
		return nil, err
	}
	return &protectedSDKFailures{dc: dc, inner: temporal.NewDefaultFailureConverter(temporal.DefaultFailureConverterOptions{DataConverter: dc})}, nil
}

// protectedSDKFailures uses SDK taxonomy while explicitly verifying attributes the SDK otherwise silently ignores.
type protectedSDKFailures struct {
	dc    ProtectedDataConverter
	inner converter.FailureConverter
}

// ProtectsFailureDetails declares the factory's mandatory protected-detail policy.
func (c *protectedSDKFailures) ProtectsFailureDetails() bool { return true }

// check validates every encoded detail before SDK conversion or borrowed failure reuse.
func (c *protectedSDKFailures) check(f *failure.Failure) error {
	if !validateFailure(f) {
		return errors.New("invalid protected failure")
	}
	checkPayloads := func(payloads *common.Payloads) error {
		if payloads == nil {
			return nil
		}
		if len(payloads.Payloads) > c.dc.DecodingLimits().MaxPayloads {
			return errors.New("protected failure details exceed limit")
		}
		for _, payload := range payloads.Payloads {
			if payload == nil {
				return errors.New("protected failure payload is absent")
			}
			if err := c.dc.ValidateProtectedPayload(payload); err != nil {
				return err
			}
		}
		return nil
	}
	for current := f; current != nil; current = current.Cause {
		if current.EncodedAttributes != nil {
			if err := c.dc.ValidateProtectedPayload(current.EncodedAttributes); err != nil {
				return err
			}
			var attributes struct {
				Message    string `json:"message"`
				StackTrace string `json:"stack_trace"`
			}
			if err := c.dc.FromPayload(current.EncodedAttributes, &attributes); err != nil {
				return err
			}
		}
		if info := current.GetApplicationFailureInfo(); info != nil {
			if err := checkPayloads(info.Details); err != nil {
				return err
			}
		}
		if info := current.GetTimeoutFailureInfo(); info != nil {
			if err := checkPayloads(info.LastHeartbeatDetails); err != nil {
				return err
			}
		}
		if info := current.GetCanceledFailureInfo(); info != nil {
			if err := checkPayloads(info.Details); err != nil {
				return err
			}
		}
		if info := current.GetResetWorkflowFailureInfo(); info != nil {
			if err := checkPayloads(info.LastHeartbeatDetails); err != nil {
				return err
			}
		}
	}
	return nil
}

// ErrorToFailure encrypts common attributes even when the SDK reuses a cached failure protobuf.
func (c *protectedSDKFailures) ErrorToFailure(err error) (result *failure.Failure) {
	defer func() {
		if recover() != nil {
			result = protocolFailure()
		}
	}()
	for cause, depth := err, 0; cause != nil; cause, depth = errors.Unwrap(cause), depth+1 {
		if depth >= 16 {
			return protocolFailure()
		}
	}
	f := c.inner.ErrorToFailure(err)
	if f == nil {
		return nil
	}
	if err := c.check(f); err != nil {
		if adapterUnavailable(err) {
			return unavailableFailure()
		}
		return protocolFailure()
	}
	f = proto.Clone(f).(*failure.Failure)
	for current := f; current != nil; current = current.Cause {
		if current.EncodedAttributes != nil {
			converter.DecodeCommonFailureAttributes(c.dc, current)
		}
		if err := converter.EncodeCommonFailureAttributes(c.dc, current); err != nil {
			if adapterUnavailable(err) {
				return unavailableFailure()
			}
			return protocolFailure()
		}
	}
	return f
}

// FailureToError rejects failed attribute authentication before SDK's permissive common-attribute decoder.
func (c *protectedSDKFailures) FailureToError(f *failure.Failure) (result error) {
	defer func() {
		if recover() != nil {
			result = temporal.NewNonRetryableApplicationError("protected failure decoding rejected", "goforj.protocol.v1", nil)
		}
	}()
	if err := c.check(f); err != nil {
		if adapterUnavailable(err) {
			return unavailableAdapterError()
		}
		return temporal.NewNonRetryableApplicationError("protected failure decoding rejected", "goforj.protocol.v1", nil)
	}
	return c.inner.FailureToError(f)
}

// WithSerializationContext preserves workflow-bound protection for native details and heartbeat recovery.
func (c *protectedSDKFailures) WithSerializationContext(ctx converter.SerializationContext) (result converter.FailureConverter) {
	defer func() {
		if recover() != nil {
			result = rejectedFailureConverter{}
		}
	}()
	dc := converter.WithDataConverterSerializationContext(c.dc, ctx)
	protected, ok := dc.(ProtectedDataConverter)
	if !ok {
		return rejectedFailureConverter{}
	}
	return &protectedSDKFailures{dc: protected, inner: temporal.NewDefaultFailureConverter(temporal.DefaultFailureConverterOptions{DataConverter: protected})}
}
