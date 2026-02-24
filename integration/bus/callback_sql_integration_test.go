//go:build integration
// +build integration

package bus_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/goforj/queue/bus"
	_ "modernc.org/sqlite"
)

func newSQLiteStoreForRuntime(t *testing.T) bus.Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "bus-runtime-store.db")
	store, err := bus.NewSQLStore(bus.SQLStoreConfig{
		DriverName: "sqlite",
		DSN:        dsn,
	})
	if err != nil {
		t.Fatalf("new sql store: %v", err)
	}
	return store
}

func TestSQLStore_RuntimeBatchThenFinallyDuplicateCallbacksSuppressed(t *testing.T) {
	q := bus.NewIntegrationTestRuntime()
	store := newSQLiteStoreForRuntime(t)

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var thenCount int
	var finallyCount int
	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })

	batchID, err := b.Batch(
		bus.NewJob("monitor:poll", nil),
	).Then(func(context.Context, bus.BatchState) error {
		thenCount++
		return nil
	}).Finally(func(context.Context, bus.BatchState) error {
		finallyCount++
		return nil
	}).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	if thenCount != 1 {
		t.Fatalf("expected then once, got %d", thenCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}

	for _, callbackKind := range []string{"batch_then", "batch_finally"} {
		cbPayload := map[string]any{
			"schema_version": 1,
			"dispatch_id":    "dup-dispatch",
			"kind":           "callback",
			"job_id":         "dup-job-" + callbackKind,
			"batch_id":       batchID,
			"callback_kind":  callbackKind,
		}
		if err := q.DispatchJSON(context.Background(), bus.InternalCallbackJobTypeForIntegration(), cbPayload); err != nil {
			t.Fatalf("dispatch duplicate callback (%s): %v", callbackKind, err)
		}
	}
	if thenCount != 1 {
		t.Fatalf("expected then to remain once after duplicate callbacks, got %d", thenCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally to remain once after duplicate callbacks, got %d", finallyCount)
	}
}

func TestSQLStore_RuntimeBatchCatchDuplicateCallbackSuppressed(t *testing.T) {
	q := bus.NewIntegrationTestRuntime()
	store := newSQLiteStoreForRuntime(t)

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var catchCount int
	b.Register("monitor:downsample", func(context.Context, bus.Context) error { return errors.New("boom") })
	batchID, err := b.Batch(
		bus.NewJob("monitor:downsample", nil),
	).Catch(func(context.Context, bus.BatchState, error) error {
		catchCount++
		return nil
	}).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected batch dispatch error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once after failed batch, got %d", catchCount)
	}

	cbPayload := map[string]any{
		"schema_version": 1,
		"dispatch_id":    "dup-dispatch",
		"kind":           "callback",
		"job_id":         "dup-job-batch-catch",
		"batch_id":       batchID,
		"callback_kind":  "batch_catch",
		"error":          "boom",
	}
	if err := q.DispatchJSON(context.Background(), bus.InternalCallbackJobTypeForIntegration(), cbPayload); err != nil {
		t.Fatalf("dispatch duplicate callback: %v", err)
	}
	if catchCount != 1 {
		t.Fatalf("expected catch to remain once after duplicate callback, got %d", catchCount)
	}
}
