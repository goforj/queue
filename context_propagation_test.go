package queue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// propagationKey separates transport metadata from receiver-owned values in these contract tests.
type propagationKey string

// testContextPropagator supplies deterministic carrier values without introducing a tracing SDK dependency.
type testContextPropagator struct{ headers map[string]string }

// Inject returns the caller-controlled metadata used to check snapshot and input limits.
func (p testContextPropagator) Inject(context.Context) map[string]string { return p.headers }

// Extract restores only the selected correlation key onto the receiver context.
func (testContextPropagator) Extract(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, propagationKey("trace"), headers["traceparent"])
}

// TestContextPropagationPreservesPayloadAndReceiverLifetime checks the serialized boundary rather than in-process context sharing.
func TestContextPropagationPreservesPayloadAndReceiverLifetime(t *testing.T) {
	headers := map[string]string{"traceparent": "parent-span", "tracestate": "vendor=value"}
	common := queueCommon{contextPropagator: testContextPropagator{headers: headers}}
	original := DriverWithMetadata(NewJob("orders.process").Payload([]byte("exact application bytes")), DriverJobMetadata{SchemaVersion: DriverJobMetadataVersion, DispatchID: "dispatch", JobID: "job"})
	published := common.dispatchContext(context.Background(), original)
	metadata := DriverMetadata(published)
	headers["traceparent"] = "mutated"
	if metadata.Context == "" || metadata.DispatchID != "dispatch" || metadata.JobID != "job" || string(published.PayloadBytes()) != "exact application bytes" {
		t.Fatalf("dispatch changed application data: %+v", metadata)
	}
	if DriverMetadata(original).Context != "" {
		t.Fatal("dispatch mutated the reusable job")
	}
	wire, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DriverJobMetadata
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	delivered := DriverWithMetadata(NewJob("orders.process").Payload(published.PayloadBytes()), decoded)
	deadline := time.Now().Add(time.Hour)
	receiver, cancel := context.WithDeadline(context.WithValue(context.Background(), propagationKey("receiver"), "retained"), deadline)
	defer cancel()
	extracted := common.deliveryContext(receiver, delivered)
	if extracted.Value(propagationKey("trace")) != "parent-span" || extracted.Value(propagationKey("receiver")) != "retained" {
		t.Fatal("carrier snapshot or receiver values were lost")
	}
	if got, ok := extracted.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatal("receiver deadline was replaced")
	}
	cancel()
	if extracted.Err() != context.Canceled {
		t.Fatal("receiver cancellation was detached")
	}
}

// TestContextPropagationRejectsInvalidCarrier keeps malformed telemetry from rejecting otherwise valid jobs.
func TestContextPropagationRejectsInvalidCarrier(t *testing.T) {
	common := queueCommon{contextPropagator: testContextPropagator{}}
	ctx := context.Background()
	tooMany := map[string]string{}
	for index := range 33 {
		tooMany[strings.Repeat("x", index+1)] = "value"
	}
	encoded, _ := json.Marshal(tooMany)
	for _, carrier := range []string{"", "null", "{}", "{", `{"traceparent":42}`, strings.Repeat("x", 8193), string(encoded)} {
		job := DriverWithMetadata(NewJob("orders.process"), DriverJobMetadata{SchemaVersion: DriverJobMetadataVersion, Context: carrier})
		if got := common.deliveryContext(ctx, job); got != ctx {
			t.Fatalf("invalid carrier changed delivery context: %q", carrier)
		}
	}
	for _, headers := range []map[string]string{nil, {}, tooMany, {"traceparent": strings.Repeat("x", 8192)}, {"traceparent": strings.Repeat("\n", 5000)}} {
		common.contextPropagator = testContextPropagator{headers: headers}
		if job := common.dispatchContext(ctx, NewJob("orders.process")); DriverMetadata(job).Context != "" {
			t.Fatal("oversized or empty carrier was persisted")
		}
	}
	common.contextPropagator = nil
	job := NewJob("orders.process")
	if got := common.deliveryContext(ctx, job); got != ctx {
		t.Fatal("disabled propagation changed delivery")
	}
	if got := common.dispatchContext(ctx, job); DriverMetadata(got).Context != "" {
		t.Fatal("disabled propagation persisted metadata")
	}
}

// TestContextPropagationOptionReachesLogicalHandlers covers the actual queue execution seam and legacy envelope compatibility.
func TestContextPropagationOptionReachesLogicalHandlers(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		options := []Option{WithContextPropagator(testContextPropagator{headers: map[string]string{"traceparent": "producer"}})}
		if legacy {
			options = append(options, WithLegacyDirectEnvelope())
		}
		q, err := NewSync(options...)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.StartWorkers(context.Background()); err != nil {
			t.Fatal(err)
		}
		calls := 0
		q.Register("orders.process", func(ctx context.Context, message Message) error {
			calls++
			if ctx.Value(propagationKey("trace")) != "producer" || string(message.PayloadBytes()) != `{"value":"exact"}` {
				t.Error("handler lost propagated context or exact payload")
			}
			return nil
		})
		if _, err := q.Dispatch(NewJob("orders.process").Payload([]byte(`{"value":"exact"}`))); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("handler calls=%d", calls)
		}
		if err := q.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
