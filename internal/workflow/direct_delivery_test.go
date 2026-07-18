package workflow

import (
	"bytes"
	"context"
	"testing"

	"github.com/goforj/queue/busruntime"
)

// TestDirectDeliveryMetadataTrustBoundary verifies the engine trusts only the
// one metadata version it owns while direct application bytes remain usable.
func TestDirectDeliveryMetadataTrustBoundary(t *testing.T) {
	t.Run("canonical dispatch", func(t *testing.T) {
		runtime := newDirectTestRuntime()
		engine, err := New(runtime)
		if err != nil {
			t.Fatalf("new engine: %v", err)
		}
		var received Context
		engine.Register("reports:build", func(_ context.Context, message Context) error {
			received = message
			return nil
		})

		result, err := engine.DispatchDirect(context.Background(), StoredJob{
			Type:    "reports:build",
			Payload: []byte{0, 1, 0xff},
			Options: JobOptions{Queue: "critical"},
		})
		if err != nil {
			t.Fatalf("dispatch direct job: %v", err)
		}
		if received.SchemaVersion != schemaVersion || received.DispatchID != result.DispatchID || received.JobID == "" {
			t.Fatalf("canonical correlation = %+v, receipt = %+v", received, result)
		}
		if received.JobType != "reports:build" || !bytes.Equal(received.PayloadBytes(), []byte{0, 1, 0xff}) {
			t.Fatalf("canonical application message = %+v payload=%v", received, received.PayloadBytes())
		}
	})

	tests := []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing", ctx: context.Background()},
		{
			name: "future version",
			ctx: busruntime.WithDeliveryMetadata(context.Background(), busruntime.DeliveryMetadata{
				SchemaVersion: busruntime.DeliveryMetadataVersion + 1,
				DispatchID:    "spoofed",
				JobID:         "spoofed",
			}),
		},
		{
			name: "unversioned fields",
			ctx: busruntime.WithDeliveryMetadata(context.Background(), busruntime.DeliveryMetadata{
				DispatchID: "spoofed",
				JobID:      "spoofed",
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newDirectTestRuntime()
			engine, err := New(runtime)
			if err != nil {
				t.Fatalf("new engine: %v", err)
			}
			var received Context
			engine.Register("reports:build", func(_ context.Context, message Context) error {
				received = message
				return nil
			})
			handler := runtime.handlers["reports:build"]
			if handler == nil {
				t.Fatal("direct application handler was not registered")
			}
			if err := handler(test.ctx, testInboundJob{payload: []byte{4, 5, 6}}); err != nil {
				t.Fatalf("handle untrusted delivery: %v", err)
			}
			if received.DispatchID != "" || received.JobID != "" || received.ChainID != "" || received.BatchID != "" {
				t.Fatalf("untrusted delivery supplied correlation: %+v", received)
			}
			if received.SchemaVersion != schemaVersion || received.JobType != "reports:build" || !bytes.Equal(received.PayloadBytes(), []byte{4, 5, 6}) {
				t.Fatalf("untrusted delivery lost application identity: %+v payload=%v", received, received.PayloadBytes())
			}
		})
	}
}

// TestDirectDeliveryFallsBackToLegacyRuntime proves custom runtimes can adopt
// the new engine without implementing direct dispatch in the same release.
func TestDirectDeliveryFallsBackToLegacyRuntime(t *testing.T) {
	runtime := newSyncTestRuntime()
	engine, err := New(runtime)
	if err != nil {
		t.Fatalf("new legacy runtime engine: %v", err)
	}
	var received Context
	engine.Register("reports:legacy-runtime", func(_ context.Context, message Context) error {
		received = message
		return nil
	})

	result, err := engine.DispatchDirect(context.Background(), StoredJob{
		Type:    "reports:legacy-runtime",
		Payload: []byte{9, 8, 7},
	})
	if err != nil {
		t.Fatalf("dispatch through legacy runtime: %v", err)
	}
	if received.DispatchID != result.DispatchID || received.JobID == "" || received.JobType != "reports:legacy-runtime" {
		t.Fatalf("legacy-runtime correlation = %+v, receipt = %+v", received, result)
	}
	if !bytes.Equal(received.PayloadBytes(), []byte{9, 8, 7}) {
		t.Fatalf("legacy-runtime payload = %v", received.PayloadBytes())
	}
}
