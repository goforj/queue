package temporalqueue

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// decodingSpy records whether untrusted input reached application decoding.
type decodingSpy struct {
	converter.DataConverter
	calls           atomic.Int32
	limits          SerializationLimits
	panicDecode     bool
	oversizedEncode bool
}

// DecodingLimits declares the inner decoder's own enforced expansion budget.
func (s *decodingSpy) DecodingLimits() SerializationLimits { return s.limits }

// FromPayload exposes only entry count so rejected payloads cannot hide a decoder invocation.
func (s *decodingSpy) FromPayload(payload *common.Payload, value any) error {
	s.calls.Add(1)
	if s.panicDecode {
		panic("private panic text")
	}
	return s.DataConverter.FromPayload(payload, value)
}

// ToPayload permits encoded-output boundary controls without allocating oversized decoded objects.
func (s *decodingSpy) ToPayload(value any) (*common.Payload, error) {
	if s.oversizedEncode {
		return &common.Payload{Data: make([]byte, s.limits.MaxPayloadBytes+1)}, nil
	}
	return s.DataConverter.ToPayload(value)
}

// TestConverterPreflightRejectsBeforeCustomDecode covers malformed, count and byte limits.
func TestConverterPreflightRejectsBeforeCustomDecode(t *testing.T) {
	limits := DefaultSerializationLimits()
	spy := &decodingSpy{DataConverter: converter.GetDefaultDataConverter(), limits: limits}
	guarded, err := newGuardedConverter(spy, limits, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	var value any
	for name, payload := range map[string]*common.Payload{"nil": nil, "bytes": {Data: make([]byte, limits.MaxPayloadBytes+1)}} {
		t.Run(name, func(t *testing.T) {
			if err := guarded.FromPayload(payload, &value); err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
	payloads := &common.Payloads{Payloads: make([]*common.Payload, limits.MaxPayloads+1)}
	if err := guarded.FromPayloads(payloads, &value); err == nil {
		t.Fatal("excessive payload count accepted")
	}
	if spy.calls.Load() != 0 {
		t.Fatal("preflight called custom decoder")
	}
	spy.oversizedEncode = true
	if _, err := guarded.ToPayload("value"); err == nil {
		t.Fatal("oversized encoding accepted")
	}
}

// TestConverterPanicAndLateCallsAreRedacted verifies adapter failures do not expose payload diagnostics.
func TestConverterPanicAndLateCallsAreRedacted(t *testing.T) {
	limits := DefaultSerializationLimits()
	spy := &decodingSpy{DataConverter: converter.GetDefaultDataConverter(), limits: limits, panicDecode: true}
	gate := newAdmission()
	guarded, err := newGuardedConverter(spy, limits, gate)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := converter.GetDefaultDataConverter().ToPayload("private payload")
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := guarded.FromPayload(payload, &value); err == nil || !strings.Contains(err.Error(), "Temporal adapter rejected input") {
		t.Fatalf("unsafe adapter error %v", err)
	}
	gate.close()
	if _, err := guarded.ToPayload("private late payload"); !errors.Is(err, errAdmissionClosed) {
		t.Fatal(err)
	}
	if guarded.ToString(payload) != "<redacted>" || guarded.ToStrings(&common.Payloads{Payloads: []*common.Payload{payload}})[0] != "<redacted>" {
		t.Fatal("payload diagnostic was exposed")
	}
}

// TestClientRejectsUnapprovedConverters verifies public construction fails before any SDK connection.
func TestClientRejectsUnapprovedConverters(t *testing.T) {
	if binding, err := NewClientBinding(client.Options{DataConverter: converter.GetDefaultDataConverter()}, SerializationLimits{}); err == nil {
		_ = binding.Close(t.Context())
		t.Fatal("unapproved custom converter accepted")
	}
	limits := DefaultSerializationLimits()
	limits.MaxPayloads = 0
	if _, err := NewClientBinding(client.Options{}, limits); err == nil {
		t.Fatal("invalid serialization budget accepted")
	}
	limits = DefaultSerializationLimits()
	inner := limits
	inner.MaxTotalBytes++
	spy := &decodingSpy{DataConverter: converter.GetDefaultDataConverter(), limits: inner}
	if _, err := NewClientBinding(client.Options{DataConverter: spy}, limits); err == nil {
		t.Fatal("decoder with larger internal budget accepted")
	}
}

// MaxEncodedOverhead declares the spy's default JSON metadata budget.
func (s *decodingSpy) MaxEncodedOverhead() int { return 256 }
