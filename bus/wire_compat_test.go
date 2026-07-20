package bus_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/busruntime"
)

var legacyWorkflowIDPattern = regexp.MustCompile(`\b(?:dsp|job|chn|bat|n)_[0-9a-f]{16}\b`)

type legacyWireCall struct {
	jobType string
	payload []byte
	options busruntime.JobOptions
}

type legacyWireRuntime struct {
	handlers map[string]busruntime.Handler
	calls    []legacyWireCall
	execute  bool
}

// newLegacyWireRuntime creates a transport recorder that can optionally execute registered workflow deliveries inline.
func newLegacyWireRuntime(execute bool) *legacyWireRuntime {
	return &legacyWireRuntime{
		handlers: make(map[string]busruntime.Handler),
		execute:  execute,
	}
}

// BusRegister retains the exact physical handler names selected by the workflow engine.
func (r *legacyWireRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	r.handlers[jobType] = handler
}

// BusDispatch records bytes before optional execution so the fixture observes the transport boundary.
func (r *legacyWireRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, options busruntime.JobOptions) error {
	r.calls = append(r.calls, legacyWireCall{
		jobType: jobType,
		payload: append([]byte(nil), payload...),
		options: options,
	})
	if !r.execute {
		return nil
	}
	handler := r.handlers[jobType]
	if handler == nil {
		return errors.New("legacy wire runtime handler is not registered")
	}
	return handler(ctx, legacyInboundJob{payload: payload})
}

// StartWorkers is inert because compatibility fixtures execute only at the SPI boundary.
func (r *legacyWireRuntime) StartWorkers(context.Context) error { return nil }

// Shutdown is inert because compatibility fixtures own no asynchronous resources.
func (r *legacyWireRuntime) Shutdown(context.Context) error { return nil }

type legacyInboundJob struct {
	payload []byte
}

// Bind decodes the recorded workflow delivery exactly as a physical worker would.
func (j legacyInboundJob) Bind(dst any) error {
	return json.Unmarshal(j.payload, dst)
}

// PayloadBytes returns an isolated copy so handlers cannot mutate recorded compatibility evidence.
func (j legacyInboundJob) PayloadBytes() []byte {
	return append([]byte(nil), j.payload...)
}

type frozenV1Envelope struct {
	SchemaVersion int         `json:"schema_version"`
	DispatchID    string      `json:"dispatch_id"`
	Kind          string      `json:"kind"`
	JobID         string      `json:"job_id"`
	ChainID       string      `json:"chain_id"`
	BatchID       string      `json:"batch_id"`
	NodeID        string      `json:"node_id"`
	Attempt       int         `json:"attempt"`
	Job           frozenV1Job `json:"job"`
	CallbackKind  string      `json:"callback_kind"`
	Error         string      `json:"error"`
}

type frozenV1Job struct {
	Type    string            `json:"type"`
	Payload []byte            `json:"payload"`
	Options frozenV1JobOption `json:"options"`
}

type frozenV1JobOption struct {
	Queue     string
	Delay     time.Duration
	Timeout   time.Duration
	Retry     int
	Backoff   time.Duration
	UniqueFor time.Duration
}

type fixedJSONPayload struct{}

// MarshalJSON pins deferred legacy payload encoding independently of the payload's Go representation.
func (fixedJSONPayload) MarshalJSON() ([]byte, error) {
	return []byte(`{"custom":true}`), nil
}

type failingJSONPayload struct{}

// MarshalJSON proves legacy payload errors still occur at Dispatch rather than NewJob.
func (failingJSONPayload) MarshalJSON() ([]byte, error) {
	return nil, errors.New("compat marshal failure")
}

// TestLegacyDirectWorkflowWireV1 freezes the physical type, JSON field order, option casing, and transport options.
func TestLegacyDirectWorkflowWireV1(t *testing.T) {
	runtime := newLegacyWireRuntime(false)
	workflow, err := bus.New(runtime)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	job := bus.NewJob("compat:job", map[string]int{"id": 7}).
		OnQueue("critical").
		Delay(2 * time.Second).
		Timeout(3 * time.Second).
		Retry(4).
		Backoff(500 * time.Millisecond).
		UniqueFor(30 * time.Second)
	if _, err := workflow.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(runtime.calls) != 1 {
		t.Fatalf("physical dispatch count = %d, want 1", len(runtime.calls))
	}
	call := runtime.calls[0]
	if call.jobType != "bus:job" {
		t.Fatalf("physical type = %q, want bus:job", call.jobType)
	}
	gotJSON := legacyWorkflowIDPattern.ReplaceAllString(string(call.payload), "ID")
	wantJSON := `{"schema_version":1,"dispatch_id":"ID","kind":"job","job_id":"ID","attempt":0,"job":{"type":"compat:job","payload":"eyJpZCI6N30=","options":{"Queue":"critical","Delay":2000000000,"Timeout":3000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}}`
	if gotJSON != wantJSON {
		t.Fatalf("workflow envelope changed:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
	wantOptions := busruntime.JobOptions{
		Queue:     "critical",
		Delay:     2 * time.Second,
		Timeout:   3 * time.Second,
		Retry:     4,
		Backoff:   500 * time.Millisecond,
		UniqueFor: 30 * time.Second,
	}
	if !reflect.DeepEqual(call.options, wantOptions) {
		t.Fatalf("transport options = %+v, want %+v", call.options, wantOptions)
	}
}

// TestLegacyPayloadEncodingV1 preserves the compatibility DTO's deferred json.Marshal semantics.
func TestLegacyPayloadEncodingV1(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{name: "nil", payload: nil, want: "null"},
		{name: "map", payload: map[string]bool{"ready": true}, want: `{"ready":true}`},
		{name: "string", payload: "raw", want: `"raw"`},
		{name: "bytes", payload: []byte{0, 1, 2}, want: `"AAEC"`},
		{name: "raw message", payload: json.RawMessage(`{"raw":true}`), want: `{"raw":true}`},
		{name: "custom marshaler", payload: fixedJSONPayload{}, want: `{"custom":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := newLegacyWireRuntime(false)
			workflow, err := bus.New(runtime)
			if err != nil {
				t.Fatalf("new bus: %v", err)
			}
			if _, err := workflow.Dispatch(context.Background(), bus.NewJob("compat:payload", tt.payload)); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			var envelope frozenV1Envelope
			if err := json.Unmarshal(runtime.calls[0].payload, &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if string(envelope.Job.Payload) != tt.want {
				t.Fatalf("encoded payload = %q, want %q", envelope.Job.Payload, tt.want)
			}
		})
	}

	runtime := newLegacyWireRuntime(false)
	workflow, err := bus.New(runtime)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	job := bus.NewJob("compat:payload", failingJSONPayload{})
	if len(runtime.calls) != 0 {
		t.Fatal("NewJob unexpectedly encoded or dispatched the payload")
	}
	if _, err := workflow.Dispatch(context.Background(), job); err == nil || err.Error() != "json: error calling MarshalJSON for type bus_test.failingJSONPayload: compat marshal failure" {
		t.Fatalf("dispatch error = %v, want deferred marshal failure", err)
	}
	if len(runtime.calls) != 0 {
		t.Fatal("marshal failure reached the physical queue")
	}
}

// TestLegacyWorkflowDeliveryNamesV1 pins every physical orchestration route and callback kind.
func TestLegacyWorkflowDeliveryNamesV1(t *testing.T) {
	tests := []struct {
		name          string
		dispatch      func(bus.Bus) error
		wantTypes     []string
		wantCallbacks []string
	}{
		{
			name: "chain success",
			dispatch: func(workflow bus.Bus) error {
				workflow.Register("compat:ok", func(context.Context, bus.Context) error { return nil })
				_, err := workflow.Chain(bus.NewJob("compat:ok", nil)).
					Finally(func(context.Context, bus.ChainState) error { return nil }).
					Dispatch(context.Background())
				return err
			},
			wantTypes:     []string{"bus:chain:node", "bus:callback"},
			wantCallbacks: []string{"", "chain_finally"},
		},
		{
			name: "chain failure",
			dispatch: func(workflow bus.Bus) error {
				workflow.Register("compat:fail", func(context.Context, bus.Context) error { return errors.New("chain failed") })
				_, err := workflow.Chain(bus.NewJob("compat:fail", nil)).
					Catch(func(context.Context, bus.ChainState, error) error { return nil }).
					Finally(func(context.Context, bus.ChainState) error { return nil }).
					Dispatch(context.Background())
				return err
			},
			wantTypes:     []string{"bus:chain:node", "bus:callback", "bus:callback"},
			wantCallbacks: []string{"", "chain_catch", "chain_finally"},
		},
		{
			name: "batch success",
			dispatch: func(workflow bus.Bus) error {
				workflow.Register("compat:ok", func(context.Context, bus.Context) error { return nil })
				_, err := workflow.Batch(bus.NewJob("compat:ok", nil)).
					Then(func(context.Context, bus.BatchState) error { return nil }).
					Finally(func(context.Context, bus.BatchState) error { return nil }).
					Dispatch(context.Background())
				return err
			},
			wantTypes:     []string{"bus:batch:job", "bus:callback", "bus:callback"},
			wantCallbacks: []string{"", "batch_then", "batch_finally"},
		},
		{
			name: "batch failure",
			dispatch: func(workflow bus.Bus) error {
				workflow.Register("compat:fail", func(context.Context, bus.Context) error { return errors.New("batch failed") })
				_, err := workflow.Batch(bus.NewJob("compat:fail", nil)).
					Catch(func(context.Context, bus.BatchState, error) error { return nil }).
					Finally(func(context.Context, bus.BatchState) error { return nil }).
					Dispatch(context.Background())
				return err
			},
			wantTypes:     []string{"bus:batch:job", "bus:callback", "bus:callback"},
			wantCallbacks: []string{"", "batch_catch", "batch_finally"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := newLegacyWireRuntime(true)
			workflow, err := bus.New(runtime)
			if err != nil {
				t.Fatalf("new bus: %v", err)
			}
			_ = tt.dispatch(workflow)
			if len(runtime.calls) != len(tt.wantTypes) {
				t.Fatalf("physical dispatch count = %d, want %d", len(runtime.calls), len(tt.wantTypes))
			}
			for i, call := range runtime.calls {
				if call.jobType != tt.wantTypes[i] {
					t.Fatalf("physical type[%d] = %q, want %q", i, call.jobType, tt.wantTypes[i])
				}
				var envelope frozenV1Envelope
				if err := json.Unmarshal(call.payload, &envelope); err != nil {
					t.Fatalf("decode envelope[%d]: %v", i, err)
				}
				if envelope.SchemaVersion != 1 {
					t.Fatalf("schema version[%d] = %d, want 1", i, envelope.SchemaVersion)
				}
				if envelope.CallbackKind != tt.wantCallbacks[i] {
					t.Fatalf("callback kind[%d] = %q, want %q", i, envelope.CallbackKind, tt.wantCallbacks[i])
				}
			}
		})
	}
}
