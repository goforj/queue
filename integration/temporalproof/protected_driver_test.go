package temporalproof

import (
	"errors"
	"strings"
	"testing"

	"github.com/goforj/queue/driver/temporalqueue"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// approvedFixtureConverter declares the bounded synthetic codec only for acceptance fixtures.
// Its public key and absent tenant AAD make it unsuitable for application data.
type approvedFixtureConverter struct{ converter.DataConverter }

// DecodingLimits reflects the fixture codec's own count and expanded-byte limits.
func (c *approvedFixtureConverter) DecodingLimits() temporalqueue.SerializationLimits {
	return temporalqueue.SerializationLimits{MaxPayloads: 4, MaxPayloadBytes: 128 << 10, MaxTotalBytes: 512 << 10}
}

// ValidateProtectedPayload rejects plaintext and malformed envelope metadata before failure decoding.
func (c *approvedFixtureConverter) ValidateProtectedPayload(payload *common.Payload) error {
	if payload == nil || string(payload.Metadata["encoding"]) != proofEncoding || proto.Size(payload) > 128<<10 {
		return errors.New("invalid protected fixture payload")
	}
	return nil
}

// TestProtectedDriverNativeFailureDetails preserves heartbeat and application detail semantics without plaintext history.
func TestProtectedDriverNativeFailureDetails(t *testing.T) {
	dc := &approvedFixtureConverter{DataConverter: encryptedProofConverter(t)}
	failures, err := temporalqueue.NewProtectedFailureConverter(dc)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := temporalqueue.NewClientBinding(client.Options{DataConverter: dc, FailureConverter: failures}, temporalqueue.SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close(t.Context())
	original := temporal.NewTimeoutError(enums.TIMEOUT_TYPE_HEARTBEAT, temporal.NewNonRetryableApplicationError("private error message", "public-type", nil, "private native detail"), "private heartbeat checkpoint")
	encoded := failures.ErrorToFailure(original)
	raw, err := protojson.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatalf("plaintext failure persisted: %s", raw)
	}
	decoded := failures.FailureToError(encoded)
	var timeout *temporal.TimeoutError
	if !errors.As(decoded, &timeout) {
		t.Fatalf("native timeout lost: %T", decoded)
	}
	var checkpoint string
	if err := timeout.LastHeartbeatDetails(&checkpoint); err != nil || checkpoint != "private heartbeat checkpoint" {
		t.Fatalf("heartbeat checkpoint %q %v", checkpoint, err)
	}
	var application *temporal.ApplicationError
	if !errors.As(decoded, &application) || application.Type() != "public-type" || !application.NonRetryable() {
		t.Fatalf("native retry metadata lost: %v", decoded)
	}
	var detail string
	if err := application.Details(&detail); err != nil || detail != "private native detail" {
		t.Fatalf("native detail %q %v", detail, err)
	}
	broken := proto.Clone(encoded).(*failure.Failure)
	broken.EncodedAttributes.Data[len(broken.EncodedAttributes.Data)-1] ^= 1
	rejected := failures.FailureToError(broken)
	if !errors.As(rejected, &application) || application.Type() != "goforj.protocol.v1" || !application.NonRetryable() {
		t.Fatal("failed attribute authentication was silently ignored")
	}
}

// MaxEncodedOverhead includes the fixture's AEAD tag, nonce and protobuf metadata.
func (c *approvedFixtureConverter) MaxEncodedOverhead() int { return 256 }
