package temporalqueue

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"
)

// failureCodecFixture uses a deliberately public test key, never application key material.
type failureCodecFixture struct{ aead cipher.AEAD }

// Encode applies authenticated protection only to small synthetic failure payloads.
func (c *failureCodecFixture) Encode(values []*common.Payload) ([]*common.Payload, error) {
	if len(values) > 4 {
		return nil, errors.New("fixture count exceeded")
	}
	out := make([]*common.Payload, len(values))
	for i, value := range values {
		if value == nil || proto.Size(value) > 4096 {
			return nil, errors.New("fixture bytes exceeded")
		}
		raw, err := proto.Marshal(value)
		if err != nil {
			return nil, err
		}
		out[i] = &common.Payload{Metadata: map[string][]byte{"encoding": []byte("fixture/aead")}, Data: c.aead.Seal(nil, nil, raw, nil)}
	}
	return out, nil
}

// Decode verifies authentication and bounds expanded data before protobuf parsing.
func (c *failureCodecFixture) Decode(values []*common.Payload) ([]*common.Payload, error) {
	if len(values) > 4 {
		return nil, errors.New("fixture count exceeded")
	}
	out := make([]*common.Payload, len(values))
	for i, value := range values {
		if value == nil || string(value.Metadata["encoding"]) != "fixture/aead" || len(value.Data) > 4096+c.aead.Overhead() {
			return nil, errors.New("fixture envelope rejected")
		}
		raw, err := c.aead.Open(nil, nil, value.Data, nil)
		if err != nil {
			return nil, errors.New("fixture authentication failed")
		}
		if len(raw) > 4096 {
			return nil, errors.New("fixture expansion rejected")
		}
		out[i] = &common.Payload{}
		if err := proto.Unmarshal(raw, out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// protectedFailureFixture controls policy, authentication and contextual-copy failures independently of SDK error taxonomy.
type protectedFailureFixture struct {
	converter.DataConverter
	mode     string
	overhead int
	limits   SerializationLimits
}

// DecodingLimits mirrors the count and expansion limits enforced by the test codec.
func (c *protectedFailureFixture) DecodingLimits() SerializationLimits { return c.limits }

// MaxEncodedOverhead exposes constructor policy failures without invoking native conversion.
func (c *protectedFailureFixture) MaxEncodedOverhead() int {
	if c.mode == "policy panic" {
		panic("private policy diagnostic")
	}
	return c.overhead
}

// ValidateProtectedPayload rejects unprotected details before they reach the SDK's permissive decoder.
func (c *protectedFailureFixture) ValidateProtectedPayload(value *common.Payload) error {
	switch c.mode {
	case "validation panic":
		panic("private validation diagnostic")
	case "validation unavailable":
		return ErrAdapterUnavailable
	case "validation error":
		return errors.New("private validation diagnostic")
	}
	if value == nil || string(value.Metadata["encoding"]) != "fixture/aead" || proto.Size(value) > 4352 {
		return errors.New("fixture envelope invalid")
	}
	return nil
}

// ToPayload exposes encoding outages at the common-attribute persistence boundary.
func (c *protectedFailureFixture) ToPayload(value any) (*common.Payload, error) {
	if c.mode == "encode unavailable" {
		return nil, ErrAdapterUnavailable
	}
	if c.mode == "encode error" {
		return nil, errors.New("private encoding diagnostic")
	}
	if c.mode == "encode panic" {
		panic("private encoding diagnostic")
	}
	return c.DataConverter.ToPayload(value)
}

// FromPayload exposes key-service availability separately from failed payload authentication.
func (c *protectedFailureFixture) FromPayload(value *common.Payload, target any) error {
	if c.mode == "decode unavailable" {
		return ErrAdapterUnavailable
	}
	if c.mode == "decode panic" {
		panic("private decoding diagnostic")
	}
	return c.DataConverter.FromPayload(value, target)
}

// WithSerializationContext deliberately loses policy only for a negative contextual-copy control.
func (c *protectedFailureFixture) WithSerializationContext(converter.SerializationContext) converter.DataConverter {
	if c.mode == "context panic" {
		panic("private context diagnostic")
	}
	if c.mode == "lost policy" {
		return converter.GetDefaultDataConverter()
	}
	return c
}

// newFailureProtectionFixture constructs bounded authenticated conversion using public synthetic test material.
func newFailureProtectionFixture(t *testing.T) *protectedFailureFixture {
	t.Helper()
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatal(err)
	}
	return &protectedFailureFixture{DataConverter: converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), &failureCodecFixture{aead: aead}), overhead: 256, limits: SerializationLimits{MaxPayloads: 4, MaxPayloadBytes: 4096, MaxTotalBytes: 16384}}
}

// TestProtectedFailuresPreserveDetailsAndAuthenticateCachedFailures covers SDK reuse, common attributes and tampering controls.
func TestProtectedFailuresPreserveDetailsAndAuthenticateCachedFailures(t *testing.T) {
	dc := newFailureProtectionFixture(t)
	fc, err := NewProtectedFailureConverter(dc)
	if err != nil {
		t.Fatal(err)
	}
	native := temporal.NewNonRetryableApplicationError("private native message", "domain.failure", nil, "private native detail")
	encoded := fc.ErrorToFailure(native)
	if strings.Contains(encoded.String(), "private native") || encoded.EncodedAttributes == nil || !encoded.GetApplicationFailureInfo().NonRetryable {
		t.Fatal("native failure confidentiality or policy lost")
	}
	decoded := fc.FailureToError(encoded)
	var application *temporal.ApplicationError
	if !errors.As(decoded, &application) || application.Message() != "private native message" || application.Type() != "domain.failure" || !application.NonRetryable() {
		t.Fatal("native error taxonomy lost", decoded)
	}
	var detail string
	if err := application.Details(&detail); err != nil || detail != "private native detail" {
		t.Fatal("protected details not restored", detail, err)
	}
	reused := fc.ErrorToFailure(decoded)
	if reused.EncodedAttributes == nil || strings.Contains(reused.String(), "private native") {
		t.Fatal("SDK cached failure lost protection")
	}
	if fc.ErrorToFailure(nil) != nil || fc.FailureToError(nil) != nil {
		t.Fatal("nil failure semantics changed")
	}
	damaged := proto.Clone(encoded).(*failure.Failure)
	damaged.EncodedAttributes.Data[0] ^= 0xff
	requireFailureProtocol(t, fc.FailureToError(damaged))
	for _, mode := range []string{"validation panic", "validation error", "validation unavailable", "decode panic", "decode unavailable"} {
		t.Run(mode, func(t *testing.T) {
			dc.mode = mode
			err := fc.FailureToError(encoded)
			if strings.HasSuffix(mode, "unavailable") {
				requirePropagationCategory(t, err, true)
			} else {
				requireFailureProtocol(t, err)
			}
			dc.mode = ""
		})
	}
	for _, mode := range []string{"encode unavailable", "encode error", "encode panic"} {
		t.Run(mode, func(t *testing.T) {
			dc.mode = mode
			f := fc.ErrorToFailure(errors.New("native failure"))
			info := f.GetApplicationFailureInfo()
			want := "goforj.protocol.v1"
			if mode == "encode unavailable" {
				want = "goforj.adapter-unavailable.v1"
			}
			if info == nil || info.Type != want {
				t.Fatalf("wrong persistence category: %+v", f)
			}
			dc.mode = ""
		})
	}
	var deep error = errors.New("native failure")
	for range 17 {
		deep = fmt.Errorf("wrapper: %w", deep)
	}
	if fc.ErrorToFailure(deep).GetApplicationFailureInfo().Type != "goforj.protocol.v1" {
		t.Fatal("recursive error chain accepted")
	}
}

// requireFailureProtocol checks terminal authentication rejection without relying on SDK wrapper strings.
func requireFailureProtocol(t *testing.T, err error) {
	t.Helper()
	var application *temporal.ApplicationError
	if !errors.As(err, &application) || application.Type() != "goforj.protocol.v1" || !application.NonRetryable() || strings.Contains(err.Error(), "private") {
		t.Fatal("protected failure did not fail closed", err)
	}
}

// TestProtectedFailureValidatesEveryDetailFamily checks application, cancellation, timeout and reset heartbeat details before SDK conversion.
func TestProtectedFailureValidatesEveryDetailFamily(t *testing.T) {
	dc := newFailureProtectionFixture(t)
	fc, err := NewProtectedFailureConverter(dc)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := dc.ToPayload("fixture detail")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"application", "cancellation", "timeout", "reset"} {
		t.Run(kind, func(t *testing.T) {
			for _, mode := range []string{"valid", "nil detail", "plaintext", "count"} {
				t.Run(mode, func(t *testing.T) {
					details := &common.Payloads{Payloads: []*common.Payload{payload}}
					switch mode {
					case "nil detail":
						details.Payloads[0] = nil
					case "plaintext":
						details.Payloads[0] = &common.Payload{Data: []byte("plaintext")}
					case "count":
						details.Payloads = make([]*common.Payload, 5)
						for i := range details.Payloads {
							details.Payloads[i] = payload
						}
					}
					f := &failure.Failure{}
					switch kind {
					case "application":
						f.FailureInfo = &failure.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failure.ApplicationFailureInfo{Type: "native", Details: details}}
					case "cancellation":
						f.FailureInfo = &failure.Failure_CanceledFailureInfo{CanceledFailureInfo: &failure.CanceledFailureInfo{Details: details}}
					case "timeout":
						f.FailureInfo = &failure.Failure_TimeoutFailureInfo{TimeoutFailureInfo: &failure.TimeoutFailureInfo{LastHeartbeatDetails: details}}
					case "reset":
						f.FailureInfo = &failure.Failure_ResetWorkflowFailureInfo{ResetWorkflowFailureInfo: &failure.ResetWorkflowFailureInfo{LastHeartbeatDetails: details}}
					}
					err := fc.FailureToError(f)
					if mode == "valid" {
						var app *temporal.ApplicationError
						if errors.As(err, &app) && app.Type() == "goforj.protocol.v1" {
							t.Fatal("valid protected details rejected", err)
						}
					} else {
						requireFailureProtocol(t, err)
					}
				})
			}
		})
	}
	cycle := &failure.Failure{}
	cycle.Cause = cycle
	requireFailureProtocol(t, fc.FailureToError(cycle))
}

// TestProtectedFailurePolicyAndContextCopiesRejectDowngrades checks factory validation and context-specific loss of protection.
func TestProtectedFailurePolicyAndContextCopiesRejectDowngrades(t *testing.T) {
	if _, err := NewProtectedFailureConverter(nil); err == nil {
		t.Fatal("nil protection policy accepted")
	}
	for _, mode := range []string{"policy panic", "invalid overhead", "invalid limits"} {
		t.Run(mode, func(t *testing.T) {
			dc := newFailureProtectionFixture(t)
			dc.mode = mode
			if mode == "invalid overhead" {
				dc.overhead = 4097
			}
			if mode == "invalid limits" {
				dc.limits.MaxPayloads = 0
			}
			if _, err := NewProtectedFailureConverter(dc); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid protected policy accepted or leaked diagnostics", err)
			}
		})
	}
	for _, mode := range []string{"success", "context panic", "lost policy"} {
		t.Run(mode, func(t *testing.T) {
			dc := newFailureProtectionFixture(t)
			dc.mode = mode
			fc, err := NewProtectedFailureConverter(dc)
			if err != nil {
				t.Fatal(err)
			}
			contextual := fc.(converter.FailureConverterWithSerializationContext).WithSerializationContext(converter.WorkflowSerializationContext{Namespace: "default", WorkflowID: "original"})
			f := contextual.ErrorToFailure(errors.New("native failure"))
			if mode == "success" {
				if f.EncodedAttributes == nil {
					t.Fatal("context copy lost protection")
				}
			} else if f.GetApplicationFailureInfo().Type != "goforj.protocol.v1" {
				t.Fatal("context downgrade accepted")
			}
		})
	}
}
