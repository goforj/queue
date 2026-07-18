package bus_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

type facadeContextKey struct{}

// TestBusNewWithQueueSharesCanonicalEngine proves compatibility wrappers do not construct or register a second workflow engine.
func TestBusNewWithQueueSharesCanonicalEngine(t *testing.T) {
	root, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	first, err := bus.New(root)
	if err != nil {
		t.Fatalf("new first compatibility facade: %v", err)
	}
	second, err := bus.New(root, nil)
	if err != nil {
		t.Fatalf("new second compatibility facade: %v", err)
	}
	if err := first.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers through facade: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := root.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown root queue: %v", shutdownErr)
		}
	})

	var handled atomic.Int32
	first.Register("compat:shared", func(ctx context.Context, message bus.Context) error {
		if ctx.Value(facadeContextKey{}) != "legacy-context" {
			return errors.New("legacy dispatch context was not forwarded")
		}
		var payload struct {
			ID int `json:"id"`
		}
		if err := message.Bind(&payload); err != nil {
			return err
		}
		if payload.ID != 7 && payload.ID != 8 {
			return errors.New("unexpected shared handler payload")
		}
		handled.Add(1)
		return nil
	})
	ctx := context.WithValue(context.Background(), facadeContextKey{}, "legacy-context")
	if _, err := second.Dispatch(ctx, bus.NewJob("compat:shared", map[string]int{"id": 7})); err != nil {
		t.Fatalf("dispatch through second facade: %v", err)
	}
	if _, err := root.WithContext(ctx).Dispatch(queue.NewJob("compat:shared").PayloadJSON(map[string]int{"id": 8})); err != nil {
		t.Fatalf("dispatch through root after facade registration: %v", err)
	}
	if handled.Load() != 2 {
		t.Fatalf("shared handler calls = %d, want 2", handled.Load())
	}

	first.Register("compat:step", func(context.Context, bus.Context) error { return nil })
	chainID, err := second.Chain(
		bus.NewJob("compat:step", nil),
		bus.NewJob("compat:step", nil),
	).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch legacy chain through root engine: %v", err)
	}
	chainState, err := root.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find legacy chain through root: %v", err)
	}
	if !chainState.Completed || chainState.NextIndex != 2 {
		t.Fatalf("shared chain state = %+v, want completed two-node chain", chainState)
	}

	batchID, err := root.Batch(
		queue.NewJob("compat:step"),
		queue.NewJob("compat:step"),
	).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch root batch: %v", err)
	}
	batchState, err := first.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find root batch through facade: %v", err)
	}
	if !batchState.Completed || batchState.Processed != 2 {
		t.Fatalf("shared batch state = %+v, want completed two-job batch", batchState)
	}

	if _, err := first.FindChain(context.Background(), "missing-chain"); !errors.Is(err, bus.ErrNotFound) || !errors.Is(err, queue.ErrWorkflowNotFound) {
		t.Fatalf("shared not-found identity = %v", err)
	}
}

// TestBusNewWithQueueRejectsConstructionOptions makes already-applied queue configuration explicit instead of silently ignoring it.
func TestBusNewWithQueueRejectsConstructionOptions(t *testing.T) {
	root, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	tests := []struct {
		name   string
		option bus.Option
	}{
		{name: "observer", option: bus.WithObserver(bus.ObserverFunc(func(context.Context, bus.Event) {}))},
		{name: "store", option: bus.WithStore(bus.NewMemoryStore())},
		{name: "clock", option: bus.WithClock(time.Now)},
		{name: "middleware", option: bus.WithMiddleware(bus.RetryPolicy{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := bus.New(root, test.option); !errors.Is(err, bus.ErrQueueOptionsUnsupported) {
				t.Fatalf("bus.New option error = %v, want ErrQueueOptionsUnsupported", err)
			}
		})
	}
	if _, err := bus.NewWithStore(root, bus.NewMemoryStore()); !errors.Is(err, bus.ErrQueueOptionsUnsupported) {
		t.Fatalf("bus.NewWithStore error = %v, want ErrQueueOptionsUnsupported", err)
	}
	if _, err := bus.New((*queue.Queue)(nil)); err == nil || err.Error() != "queue is required" {
		t.Fatalf("typed nil queue error = %v, want queue is required", err)
	}
}

// TestBusQueueFacadePreservesLegacyPayloadEncoding proves the new canonical route does not reinterpret compatibility DTO payloads.
func TestBusQueueFacadePreservesLegacyPayloadEncoding(t *testing.T) {
	root, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	compatibility, err := bus.New(root)
	if err != nil {
		t.Fatalf("new compatibility facade: %v", err)
	}
	if err := compatibility.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := compatibility.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown compatibility facade: %v", shutdownErr)
		}
	})

	var got []byte
	compatibility.Register("compat:payload", func(_ context.Context, message bus.Context) error {
		got = message.PayloadBytes()
		return nil
	})
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
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got = nil
			if _, err := compatibility.Dispatch(context.Background(), bus.NewJob("compat:payload", test.payload)); err != nil {
				t.Fatalf("dispatch legacy payload: %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("handler payload = %q, want %q", got, test.want)
			}
		})
	}

	if _, err := compatibility.Dispatch(context.Background(), bus.NewJob("compat:payload", failingJSONPayload{})); err == nil || err.Error() != "json: error calling MarshalJSON for type bus_test.failingJSONPayload: compat marshal failure" {
		t.Fatalf("marshal failure = %v, want legacy deferred error", err)
	}
}
