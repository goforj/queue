package bus

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/goforj/queue/busruntime"
	"strings"
	"testing"
	"time"
)

// propagationKey separates transported correlation from worker-owned values.
type propagationKey string

// testContextPropagator supplies deterministic headers without a tracing dependency.
type testContextPropagator struct{ headers map[string]string }

// Inject returns values to snapshot before enqueueing.
func (p testContextPropagator) Inject(context.Context) map[string]string { return p.headers }

// Extract restores one value onto the worker's lifetime.
func (testContextPropagator) Extract(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, propagationKey("trace"), headers["traceparent"])
}

// TestContextCarrierSnapshotAndReceiverLifetime covers the physical envelope boundary independently from local context sharing.
func TestContextCarrierSnapshotAndReceiverLifetime(t *testing.T) {
	headers := map[string]string{"traceparent": "producer"}
	runtime := runtime{propagator: testContextPropagator{headers: headers}}
	encoded := runtime.dispatchContext(context.Background())
	headers["traceparent"] = "changed"
	receiver, cancel := context.WithDeadline(context.WithValue(context.Background(), propagationKey("receiver"), "kept"), time.Now().Add(time.Hour))
	defer cancel()
	extracted := runtime.deliveryContext(receiver, encoded)
	if extracted.Value(propagationKey("trace")) != "producer" || extracted.Value(propagationKey("receiver")) != "kept" {
		t.Fatal("carrier snapshot or receiver values changed")
	}
	original, _ := receiver.Deadline()
	if actual, ok := extracted.Deadline(); !ok || !actual.Equal(original) {
		t.Fatal("receiver deadline changed")
	}
	cancel()
	if extracted.Err() != context.Canceled {
		t.Fatal("receiver cancellation changed")
	}
}

// TestContextCarrierLimitsAndMalformedInput prevents correlation failures from rejecting valid business payloads.
func TestContextCarrierLimitsAndMalformedInput(t *testing.T) {
	runtime := runtime{propagator: testContextPropagator{}}
	ctx := context.Background()
	many := map[string]string{}
	for index := range 33 {
		many[strings.Repeat("x", index+1)] = "value"
	}
	tooMany, _ := json.Marshal(many)
	for _, encoded := range []json.RawMessage{nil, []byte("null"), []byte("{}"), []byte("{"), []byte(`{"traceparent":42}`), []byte(strings.Repeat("x", 8193)), tooMany} {
		if got := runtime.deliveryContext(ctx, encoded); got != ctx {
			t.Fatal("invalid carrier changed execution")
		}
	}
	for _, headers := range []map[string]string{nil, {}, many, {"traceparent": strings.Repeat("x", 8192)}, {"traceparent": strings.Repeat("\n", 5000)}} {
		runtime.propagator = testContextPropagator{headers: headers}
		if encoded := runtime.dispatchContext(ctx); len(encoded) != 0 {
			t.Fatal("oversized carrier was persisted")
		}
	}
	runtime.propagator = nil
	if len(runtime.dispatchContext(ctx)) != 0 || runtime.deliveryContext(ctx, []byte(`{"traceparent":"ignored"}`)) != ctx {
		t.Fatal("disabled propagation changed delivery")
	}
}

// TestEnvelopeIgnoresMalformedContextTypes preserves the business envelope when optional correlation has the wrong JSON type.
func TestEnvelopeIgnoresMalformedContextTypes(t *testing.T) {
	runtime := runtime{propagator: testContextPropagator{}}
	ctx := context.Background()
	for _, value := range []string{`42`, `false`, `"invalid"`, `[1,2]`, `{"traceparent":42}`} {
		original := envelope{SchemaVersion: 1, JobID: "job", Job: wireJob{Type: "orders.process", Payload: []byte(`{"id":"exact"}`)}, Context: json.RawMessage(value)}
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var decoded envelope
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.JobID != original.JobID || decoded.Job.Type != original.Job.Type || string(decoded.Job.Payload) != string(original.Job.Payload) {
			t.Fatal("optional metadata changed business data")
		}
		if runtime.deliveryContext(ctx, decoded.Context) != ctx {
			t.Fatal("malformed context changed execution")
		}
	}
}

// isolatedPropagationRuntime discards sender context so workflow tests cross a serialized delivery boundary.
type isolatedPropagationRuntime struct{ *syncTestRuntime }

// BusDispatch simulates a broker that delivers bytes on an independently owned worker context.
func (r isolatedPropagationRuntime) BusDispatch(_ context.Context, name string, payload []byte, options busruntime.JobOptions) error {
	var err error
	for attempt := 0; attempt <= options.Retry; attempt++ {
		err = r.syncTestRuntime.BusDispatch(context.Background(), name, payload, options)
		if err == nil {
			return nil
		}
	}
	return err
}

// workflowContextPropagator requires every workflow transition to retain correlation before it can enqueue the next delivery.
type workflowContextPropagator struct{ testContextPropagator }

// Inject reads the active context so fixed fixture headers cannot conceal a missing extraction at an intermediate boundary.
func (workflowContextPropagator) Inject(ctx context.Context) map[string]string {
	identity, _ := ctx.Value(propagationKey("trace")).(string)
	if identity == "" {
		return nil
	}
	return map[string]string{"traceparent": identity}
}

// TestWorkflowContextPropagation covers chained jobs, batch jobs, and completion callbacks across serialized deliveries.
func TestWorkflowContextPropagation(t *testing.T) {
	runtime, err := New(isolatedPropagationRuntime{newSyncTestRuntime()}, WithContextPropagator(workflowContextPropagator{}))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runtime.Register("step", func(ctx context.Context, _ Context) error {
		if ctx.Value(propagationKey("trace")) != "producer" {
			t.Error("job lost producer context")
		}
		calls++
		return nil
	})
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Shutdown(context.Background())
	producer := context.WithValue(context.Background(), propagationKey("trace"), "producer")
	chainDone := false
	if _, err := runtime.Chain(NewJob("step", nil), NewJob("step", nil)).Finally(func(ctx context.Context, _ ChainState) error {
		chainDone = true
		if ctx.Value(propagationKey("trace")) != "producer" {
			t.Error("chain callback lost producer context")
		}
		return nil
	}).Dispatch(producer); err != nil {
		t.Fatal(err)
	}
	batchDone := false
	if _, err := runtime.Batch(NewJob("step", nil), NewJob("step", nil)).Then(func(ctx context.Context, _ BatchState) error {
		batchDone = true
		if ctx.Value(propagationKey("trace")) != "producer" {
			t.Error("batch callback lost producer context")
		}
		return nil
	}).Dispatch(producer); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || !chainDone || !batchDone {
		t.Fatalf("jobs=%d chain=%v batch=%v", calls, chainDone, batchDone)
	}
	attempts := 0
	runtime.Register("retry", func(ctx context.Context, _ Context) error {
		attempts++
		if ctx.Value(propagationKey("trace")) != "producer" {
			t.Error("retry lost the serialized producer identity")
		}
		if attempts == 1 {
			return errors.New("transient failure")
		}
		return nil
	})
	if _, err := runtime.Dispatch(producer, NewJob("retry", nil).Retry(1)); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("retry attempts=%d want=2", attempts)
	}
}
