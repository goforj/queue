//go:build integration

package temporal

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

const protectedTestLimit = 64 << 10
const protectedTestEncoding = "queue-proof/aes-gcm"

// protectedTestCodec is a bounded synthetic test codec, not a supported user adapter.
// Its public fixture key must never be used for application data.
type protectedTestCodec struct{ aead cipher.AEAD }

// newProtectedTestCodec constructs a codec using a deliberately public synthetic key.
func newProtectedTestCodec(t *testing.T, seed byte) *protectedTestCodec {
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
	return &protectedTestCodec{aead: aead}
}

// Encode bounds work before serializing each synthetic payload.
func (c *protectedTestCodec) Encode(payloads []*common.Payload) ([]*common.Payload, error) {
	if len(payloads) > 4 {
		return nil, errors.New("proof payload count exceeded")
	}
	encoded := make([]*common.Payload, len(payloads))
	for i, payload := range payloads {
		if payload == nil || proto.Size(payload) > protectedTestLimit {
			return nil, errors.New("proof payload size exceeded")
		}
		plaintext, err := proto.Marshal(payload)
		if err != nil {
			return nil, errors.New("proof payload encoding failed")
		}
		encoded[i] = &common.Payload{Metadata: map[string][]byte{"encoding": []byte(protectedTestEncoding)}, Data: c.aead.Seal(nil, nil, plaintext, nil)}
	}
	return encoded, nil
}

// Decode enforces ciphertext and expanded-byte bounds and sanitizes failures.
func (c *protectedTestCodec) Decode(payloads []*common.Payload) ([]*common.Payload, error) {
	if len(payloads) > 4 {
		return nil, errors.New("proof payload count exceeded")
	}
	decoded := make([]*common.Payload, len(payloads))
	for i, payload := range payloads {
		if payload == nil || string(payload.Metadata["encoding"]) != protectedTestEncoding || len(payload.Data) > protectedTestLimit+c.aead.Overhead() {
			return nil, errors.New("proof encoded payload rejected")
		}
		plaintext, err := c.aead.Open(nil, nil, payload.Data, nil)
		if err != nil {
			return nil, errors.New("proof payload authentication failed")
		}
		if len(plaintext) > protectedTestLimit {
			return nil, errors.New("proof decoded payload size exceeded")
		}
		decoded[i] = &common.Payload{}
		if err := proto.Unmarshal(plaintext, decoded[i]); err != nil {
			return nil, errors.New("proof payload decoding failed")
		}
	}
	return decoded, nil
}

// protectedDriverConverter declares the bounded public-key fixture contract for actual resource tests only.
type protectedDriverConverter struct{ converter.DataConverter }

// DecodingLimits reflects limits independently enforced by the synthetic AEAD codec.
func (*protectedDriverConverter) DecodingLimits() temporalqueue.SerializationLimits {
	return temporalqueue.SerializationLimits{MaxPayloads: 4, MaxPayloadBytes: 64 << 10, MaxTotalBytes: 256 << 10}
}

// MaxEncodedOverhead reserves nonce, AEAD tag and protobuf envelope metadata.
func (*protectedDriverConverter) MaxEncodedOverhead() int { return 256 }

// ValidateProtectedPayload forbids plaintext detail fallback.
func (*protectedDriverConverter) ValidateProtectedPayload(payload *common.Payload) error {
	if payload == nil || string(payload.Metadata["encoding"]) != protectedTestEncoding || proto.Size(payload) > protectedTestLimit+256 {
		return errors.New("invalid protected test payload")
	}
	return nil
}

// secretLogger retains actual SDK diagnostics so sentinel assertions include native logging paths.
type secretLogger struct {
	mu   sync.Mutex
	text strings.Builder
}

// Debug records adapter-visible debug diagnostics without modifying SDK control flow.
func (l *secretLogger) Debug(message string, fields ...interface{}) { l.record(message, fields...) }

// Info records adapter-visible informational diagnostics.
func (l *secretLogger) Info(message string, fields ...interface{}) { l.record(message, fields...) }

// Warn records adapter-visible warning diagnostics.
func (l *secretLogger) Warn(message string, fields ...interface{}) { l.record(message, fields...) }

// Error records adapter-visible failure diagnostics.
func (l *secretLogger) Error(message string, fields ...interface{}) { l.record(message, fields...) }

// record serializes logger access from concurrent SDK pollers.
func (l *secretLogger) record(message string, fields ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprint(&l.text, message, fields)
}

// TestTemporalDriverProtectedHistoryAndDiagnostics qualifies configured conversion on actual dispatch, hook, read and replay paths.
func TestTemporalDriverProtectedHistoryAndDiagnostics(t *testing.T) {
	requireTemporalServer(t)
	dc := &protectedDriverConverter{DataConverter: converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), newProtectedTestCodec(t, 1))}
	failures, err := temporalqueue.NewProtectedFailureConverter(dc)
	if err != nil {
		t.Fatal(err)
	}
	logger := &secretLogger{}
	name := fmt.Sprintf("driver-protected-%d", time.Now().UnixNano())
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, DataConverter: dc, FailureConverter: failures, Logger: logger}, DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	const secret = "synthetic-sensitive-payload-231791"
	q.Register("business", func(_ context.Context, m queue.Message) error {
		if string(m.PayloadBytes()) != secret {
			t.Error("business plaintext did not roundtrip")
		}
		return nil
	})
	q.Register("hook", func(_ context.Context, m queue.Message) error {
		if string(m.PayloadBytes()) != secret {
			t.Error("hook plaintext did not roundtrip")
		}
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := queue.Continue(q.Chain(queue.NewJob("business").Payload([]byte(secret)))).OnCompletion(queue.NewJob("hook").Payload([]byte(secret))).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := temporalqueue.Client(q)
	run := c.GetWorkflow(t.Context(), id, "")
	if err := run.Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	st, err := q.FindChain(t.Context(), id)
	if err != nil || !st.Completed || string(st.Nodes[0].Job.Payload) != secret {
		t.Fatalf("protected state %+v %v", st, err)
	}
	outcomes, err := q.FindContinuations(t.Context(), id)
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != "succeeded" {
		t.Fatalf("protected hooks %+v %v", outcomes, err)
	}
	iterator := c.GetWorkflowHistory(t.Context(), id, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := protojson.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(secret)))) {
			t.Fatal("secret found in persisted history")
		}
	}
	replay, err := temporalqueue.NewWorkflowReplayer(q)
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayWorkflowExecution(t.Context(), c.WorkflowService(), nil, "default", workflow.Execution{ID: id, RunID: run.GetRunID()}); err != nil {
		t.Fatal(err)
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if strings.Contains(logger.text.String(), secret) {
		t.Fatal("SDK log exposed payload sentinel")
	}
}
