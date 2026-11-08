package temporalproof

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

const proofPayloadLimit = 64 << 10
const proofEncoding = "queue-proof/aes-gcm"

// proofCodec is a bounded synthetic test codec, not a supported user adapter.
// Its public fixture key must never be used for application data.
type proofCodec struct{ aead cipher.AEAD }

// newProofCodec constructs a codec using a deliberately public synthetic key.
func newProofCodec(t *testing.T, seed byte) *proofCodec {
	t.Helper()
	key := make([]byte, 32)
	key[0] = seed
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatal(err)
	}
	return &proofCodec{aead: aead}
}

// Encode bounds work before serializing each synthetic payload.
func (c *proofCodec) Encode(payloads []*common.Payload) ([]*common.Payload, error) {
	if len(payloads) > 4 {
		return nil, errors.New("proof payload count exceeded")
	}
	encoded := make([]*common.Payload, len(payloads))
	for i, payload := range payloads {
		if payload == nil || proto.Size(payload) > proofPayloadLimit {
			return nil, errors.New("proof payload size exceeded")
		}
		plaintext, err := proto.Marshal(payload)
		if err != nil {
			return nil, errors.New("proof payload encoding failed")
		}
		encoded[i] = &common.Payload{Metadata: map[string][]byte{"encoding": []byte(proofEncoding)}, Data: c.aead.Seal(nil, nil, plaintext, nil)}
	}
	return encoded, nil
}

// Decode enforces ciphertext and expanded-byte bounds and sanitizes failures.
func (c *proofCodec) Decode(payloads []*common.Payload) ([]*common.Payload, error) {
	if len(payloads) > 4 {
		return nil, errors.New("proof payload count exceeded")
	}
	decoded := make([]*common.Payload, len(payloads))
	for i, payload := range payloads {
		if payload == nil || string(payload.Metadata["encoding"]) != proofEncoding || len(payload.Data) > proofPayloadLimit+c.aead.Overhead() {
			return nil, errors.New("proof encoded payload rejected")
		}
		plaintext, err := c.aead.Open(nil, nil, payload.Data, nil)
		if err != nil {
			return nil, errors.New("proof payload authentication failed")
		}
		if len(plaintext) > proofPayloadLimit {
			return nil, errors.New("proof decoded payload size exceeded")
		}
		decoded[i] = &common.Payload{}
		if err := proto.Unmarshal(plaintext, decoded[i]); err != nil {
			return nil, errors.New("proof payload decoding failed")
		}
	}
	return decoded, nil
}

// TestProofCodecBoundsAndAuthentication exercises the prototype codec's own
// bounds without claiming that arbitrary native SDK converters are safe.
func TestProofCodecBoundsAndAuthentication(t *testing.T) {
	codec := newProofCodec(t, 1)
	original := &common.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(`{"synthetic":true}`)}
	encoded, err := codec.Encode([]*common.Payload{original})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(encoded)
	if err != nil || !proto.Equal(original, decoded[0]) {
		t.Fatalf("codec roundtrip err=%v", err)
	}
	if _, err := newProofCodec(t, 2).Decode(encoded); err == nil {
		t.Fatal("wrong key accepted")
	}
	for _, payloads := range [][]*common.Payload{{nil}, {original, original, original, original, original}, {{Data: make([]byte, proofPayloadLimit+1)}}} {
		if _, err := codec.Encode(payloads); err == nil {
			t.Fatal("out-of-bounds input accepted by Encode")
		}
	}
	for _, payloads := range [][]*common.Payload{{nil}, {original}, {encoded[0], encoded[0], encoded[0], encoded[0], encoded[0]}, {{Metadata: map[string][]byte{"encoding": []byte(proofEncoding)}, Data: make([]byte, proofPayloadLimit+codec.aead.Overhead()+1)}}} {
		if _, err := codec.Decode(payloads); err == nil {
			t.Fatal("out-of-bounds input accepted by Decode")
		}
	}
	broken := proto.Clone(encoded[0]).(*common.Payload)
	broken.Data[len(broken.Data)-1] ^= 1
	if _, err := codec.Decode([]*common.Payload{broken}); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

// encryptedProofConverter provides consistent client/worker/read conversion for one experiment.
func encryptedProofConverter(t *testing.T) converter.DataConverter {
	return converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), newProofCodec(t, 1))
}
