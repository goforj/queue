//go:build integration

package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/workflow"
)

type integrationTestInboundJob struct {
	payload []byte
}

// Bind decodes one integration delivery into the engine envelope.
func (j integrationTestInboundJob) Bind(dst any) error {
	return json.Unmarshal(j.payload, dst)
}

// PayloadBytes returns a copy of the integration delivery payload.
func (j integrationTestInboundJob) PayloadBytes() []byte {
	return append([]byte(nil), j.payload...)
}

// IntegrationTestRuntime is a minimal in-memory runtime used by integration tests
// that need to dispatch physical workflow deliveries directly.
type IntegrationTestRuntime struct {
	handlers map[string]busruntime.Handler
}

// NewIntegrationTestRuntime creates an in-memory raw runtime for integration fixtures.
func NewIntegrationTestRuntime() *IntegrationTestRuntime {
	return &IntegrationTestRuntime{handlers: make(map[string]busruntime.Handler)}
}

// BusRegister records a physical workflow handler by delivery type.
func (r *IntegrationTestRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if r.handlers == nil {
		r.handlers = make(map[string]busruntime.Handler)
	}
	r.handlers[jobType] = handler
}

// BusDispatch invokes the physical workflow handler synchronously.
func (r *IntegrationTestRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, _ busruntime.JobOptions) error {
	h, ok := r.handlers[jobType]
	if !ok || h == nil {
		return fmt.Errorf("handler not registered for %q", jobType)
	}
	return h(ctx, integrationTestInboundJob{payload: append([]byte(nil), payload...)})
}

// StartWorkers is inert because the integration runtime dispatches synchronously.
func (r *IntegrationTestRuntime) StartWorkers(context.Context) error { return nil }

// Shutdown is inert because the integration runtime owns no asynchronous resources.
func (r *IntegrationTestRuntime) Shutdown(context.Context) error { return nil }

// DispatchJSON encodes a literal integration envelope before physical dispatch.
func (r *IntegrationTestRuntime) DispatchJSON(ctx context.Context, jobType string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.BusDispatch(ctx, jobType, b, busruntime.JobOptions{})
}

// InternalCallbackJobTypeForIntegration returns the version-one callback delivery name.
func InternalCallbackJobTypeForIntegration() string {
	return workflow.CallbackDeliveryType
}
