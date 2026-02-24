//go:build integration

package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/goforj/queue/busruntime"
)

type integrationTestInboundJob struct {
	payload []byte
}

func (j integrationTestInboundJob) Bind(dst any) error {
	return json.Unmarshal(j.payload, dst)
}

func (j integrationTestInboundJob) PayloadBytes() []byte {
	return append([]byte(nil), j.payload...)
}

// IntegrationTestRuntime is a minimal in-memory runtime used by integration tests
// that need to dispatch internal bus jobs directly.
type IntegrationTestRuntime struct {
	handlers map[string]busruntime.Handler
}

func NewIntegrationTestRuntime() *IntegrationTestRuntime {
	return &IntegrationTestRuntime{handlers: make(map[string]busruntime.Handler)}
}

func (r *IntegrationTestRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if r.handlers == nil {
		r.handlers = make(map[string]busruntime.Handler)
	}
	r.handlers[jobType] = handler
}

func (r *IntegrationTestRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, _ busruntime.JobOptions) error {
	h, ok := r.handlers[jobType]
	if !ok || h == nil {
		return fmt.Errorf("handler not registered for %q", jobType)
	}
	return h(ctx, integrationTestInboundJob{payload: append([]byte(nil), payload...)})
}

func (r *IntegrationTestRuntime) StartWorkers(context.Context) error { return nil }
func (r *IntegrationTestRuntime) Shutdown(context.Context) error     { return nil }

func (r *IntegrationTestRuntime) DispatchJSON(ctx context.Context, jobType string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.BusDispatch(ctx, jobType, b, busruntime.JobOptions{})
}

func InternalCallbackJobTypeForIntegration() string {
	return internalJobCallback
}
