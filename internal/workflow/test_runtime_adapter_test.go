package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/goforj/queue/busruntime"
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
	handlers    map[string]busruntime.Handler
	dispatchErr error
}

// directTestRuntime adds canonical direct dispatch to the retained legacy test runtime.
type directTestRuntime struct {
	*syncTestRuntime
}

type syncTestAcceptedError struct {
	cause error
}

// Error preserves the inline handler failure returned by the synchronous test runtime.
func (e syncTestAcceptedError) Error() string { return e.cause.Error() }

// Unwrap exposes the inline handler failure to errors.Is and errors.As.
func (e syncTestAcceptedError) Unwrap() error { return e.cause }

// DispatchAccepted reports that the test runtime invoked a handler after accepting its delivery.
func (e syncTestAcceptedError) DispatchAccepted() bool { return true }

func newSyncTestRuntime() *syncTestRuntime {
	return &syncTestRuntime{handlers: make(map[string]busruntime.Handler)}
}

// newDirectTestRuntime creates an inline runtime that exercises the optional
// direct-dispatch capability without changing legacy test fixtures.
func newDirectTestRuntime() *directTestRuntime {
	return &directTestRuntime{syncTestRuntime: newSyncTestRuntime()}
}

func (r *syncTestRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if r.handlers == nil {
		r.handlers = make(map[string]busruntime.Handler)
	}
	r.handlers[jobType] = handler
}

func (r *syncTestRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, _ busruntime.JobOptions) error {
	if r.dispatchErr != nil {
		return r.dispatchErr
	}
	h, ok := r.handlers[jobType]
	if !ok || h == nil {
		return fmt.Errorf("handler not registered for %q", jobType)
	}
	if err := h(ctx, testInboundJob{payload: append([]byte(nil), payload...)}); err != nil {
		return syncTestAcceptedError{cause: err}
	}
	return nil
}

// BusDispatchDirect carries direct metadata beside the application payload in
// the same way a compatible physical runtime presents it to the engine.
func (r *directTestRuntime) BusDispatchDirect(ctx context.Context, jobType string, payload []byte, metadata busruntime.DeliveryMetadata, opts busruntime.JobOptions) error {
	return r.BusDispatch(busruntime.WithDeliveryMetadata(ctx, metadata), jobType, payload, opts)
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

// TestRuntimeNilHandlerRegistrationIsNoop verifies direct runtimes never receive an executable target for a nil application handler.
func TestRuntimeNilHandlerRegistrationIsNoop(t *testing.T) {
	transport := newDirectTestRuntime()
	engine, err := New(transport)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	const jobType = "workflow:nil-registration"
	engine.Register(jobType, nil)
	if _, registered := transport.handlers[jobType]; registered {
		t.Fatal("nil handler created a physical direct-delivery registration")
	}
	if _, err := engine.DispatchDirect(context.Background(), StoredJob{Type: jobType}); err == nil {
		t.Fatal("nil registration accepted a direct delivery")
	} else if !strings.Contains(err.Error(), "handler not registered") {
		t.Fatalf("direct dispatch error = %v, want missing handler", err)
	}

	handlerCalls := 0
	engine.Register(jobType, func(context.Context, Context) error {
		handlerCalls++
		return nil
	})
	engine.Register(jobType, nil)
	if _, err := engine.DispatchDirect(context.Background(), StoredJob{Type: jobType}); err != nil {
		t.Fatalf("dispatch after nil replacement: %v", err)
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
}
