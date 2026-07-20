//go:build integration

package bus

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/workflow"
)

// integrationHookPayload gives both direct bytes and JSON dispatch a concrete bind target.
type integrationHookPayload struct {
	Value int `json:"value"`
}

// integrationHookMarshalFailure makes the integration hook's encoding failure deterministic.
type integrationHookMarshalFailure struct {
	err error
}

// MarshalJSON returns the configured error so DispatchJSON's encoding boundary remains observable.
func (p integrationHookMarshalFailure) MarshalJSON() ([]byte, error) {
	return nil, p.err
}

// TestIntegrationTestRuntimeExercisesPhysicalDispatchHooks verifies the
// integration-only runtime preserves registration, payload, and error behavior.
func TestIntegrationTestRuntimeExercisesPhysicalDispatchHooks(t *testing.T) {
	runtime := NewIntegrationTestRuntime()
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start integration runtime: %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown integration runtime: %v", err)
	}
	if err := runtime.BusDispatch(context.Background(), "missing", nil, busruntime.JobOptions{}); err == nil {
		t.Fatal("missing integration handler was accepted")
	}
	runtime.BusRegister("nil-handler", nil)
	if err := runtime.BusDispatch(context.Background(), "nil-handler", nil, busruntime.JobOptions{}); err == nil {
		t.Fatal("nil integration handler was accepted")
	}

	var zeroValueRuntime IntegrationTestRuntime
	var (
		delivered busruntime.InboundJob
		got       integrationHookPayload
	)
	zeroValueRuntime.BusRegister("integration:payload", func(_ context.Context, job busruntime.InboundJob) error {
		delivered = job
		first := job.PayloadBytes()
		first[0] = '!'
		if string(job.PayloadBytes()) != `{"value":7}` {
			return errors.New("integration payload bytes were not isolated")
		}
		if err := job.Bind(&got); err != nil {
			return err
		}
		return nil
	})
	rawPayload := []byte(`{"value":7}`)
	if err := zeroValueRuntime.BusDispatch(context.Background(), "integration:payload", rawPayload, busruntime.JobOptions{}); err != nil {
		t.Fatalf("dispatch integration bytes: %v", err)
	}
	if string(rawPayload) != `{"value":7}` || got.Value != 7 {
		t.Fatalf("raw integration payload/bound value = %q/%+v", rawPayload, got)
	}
	rawPayload[0] = '!'
	if string(delivered.PayloadBytes()) != `{"value":7}` {
		t.Fatal("integration delivery retained the caller's mutable payload")
	}
	got = integrationHookPayload{}
	if err := zeroValueRuntime.DispatchJSON(context.Background(), "integration:payload", integrationHookPayload{Value: 7}); err != nil {
		t.Fatalf("dispatch integration JSON: %v", err)
	}
	if got.Value != 7 {
		t.Fatalf("bound integration payload = %+v", got)
	}

	marshalErr := errors.New("integration payload encoding failed")
	if err := zeroValueRuntime.DispatchJSON(context.Background(), "integration:payload", integrationHookMarshalFailure{err: marshalErr}); !errors.Is(err, marshalErr) {
		t.Fatalf("DispatchJSON encoding error = %v, want %v", err, marshalErr)
	}
	if got := InternalCallbackJobTypeForIntegration(); got != workflow.CallbackDeliveryType {
		t.Fatalf("callback delivery type = %q, want %q", got, workflow.CallbackDeliveryType)
	}
}
