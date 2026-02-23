package bus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/goforj/queue/internal/busruntime"
)

type testInboundJob struct {
	payload []byte
}

func (j testInboundJob) Bind(dst any) error {
	return json.Unmarshal(j.payload, dst)
}

func (j testInboundJob) PayloadBytes() []byte {
	return append([]byte(nil), j.payload...)
}

type syncTestRuntime struct {
	handlers map[string]busruntime.Handler
}

func newSyncTestRuntime() *syncTestRuntime {
	return &syncTestRuntime{handlers: make(map[string]busruntime.Handler)}
}

func (r *syncTestRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if r.handlers == nil {
		r.handlers = make(map[string]busruntime.Handler)
	}
	r.handlers[jobType] = handler
}

func (r *syncTestRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, _ busruntime.JobOptions) error {
	h, ok := r.handlers[jobType]
	if !ok || h == nil {
		return fmt.Errorf("handler not registered for %q", jobType)
	}
	return h(ctx, testInboundJob{payload: append([]byte(nil), payload...)})
}

func (r *syncTestRuntime) StartWorkers(context.Context) error { return nil }
func (r *syncTestRuntime) Shutdown(context.Context) error     { return nil }

func (r *syncTestRuntime) DispatchJSON(ctx context.Context, jobType string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.BusDispatch(ctx, jobType, b, busruntime.JobOptions{})
}
