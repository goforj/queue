//go:build integration
// +build integration

package bus_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/busruntime"
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

type failFirstCallbackKindRuntime struct {
	inner      *bus.IntegrationTestRuntime
	kind       string
	failedOnce atomic.Bool
}

func newFailFirstCallbackKindRuntime(kind string) *failFirstCallbackKindRuntime {
	return &failFirstCallbackKindRuntime{
		inner: bus.NewIntegrationTestRuntime(),
		kind:  kind,
	}
}

func (r *failFirstCallbackKindRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	r.inner.BusRegister(jobType, handler)
}

func (r *failFirstCallbackKindRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	if jobType == bus.InternalCallbackJobTypeForIntegration() && !r.failedOnce.Load() {
		var env struct {
			CallbackKind string `json:"callback_kind"`
		}
		if err := json.Unmarshal(payload, &env); err == nil && env.CallbackKind == r.kind {
			r.failedOnce.Store(true)
			return errors.New("injected callback dispatch failure")
		}
	}
	return r.inner.BusDispatch(ctx, jobType, payload, opts)
}

func (r *failFirstCallbackKindRuntime) StartWorkers(ctx context.Context) error { return r.inner.StartWorkers(ctx) }
func (r *failFirstCallbackKindRuntime) Shutdown(ctx context.Context) error     { return r.inner.Shutdown(ctx) }

func (r *failFirstCallbackKindRuntime) DispatchJSON(ctx context.Context, jobType string, payload any) error {
	return r.inner.DispatchJSON(ctx, jobType, payload)
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

func TestSQLStore_RuntimeChainFinallyDuplicateCallbackSuppressed(t *testing.T) {
	q := bus.NewIntegrationTestRuntime()
	store := newSQLiteStoreForRuntime(t)

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var finallyCount int
	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })

	chainID, err := b.Chain(
		bus.NewJob("monitor:poll", nil),
	).Finally(func(context.Context, bus.ChainState) error {
		finallyCount++
		return nil
	}).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}

	cbPayload := map[string]any{
		"schema_version": 1,
		"dispatch_id":    "dup-dispatch",
		"kind":           "callback",
		"job_id":         "dup-job-chain-finally",
		"chain_id":       chainID,
		"callback_kind":  "chain_finally",
	}
	if err := q.DispatchJSON(context.Background(), bus.InternalCallbackJobTypeForIntegration(), cbPayload); err != nil {
		t.Fatalf("dispatch duplicate callback: %v", err)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally to remain once after duplicate callback, got %d", finallyCount)
	}
}

func TestSQLStore_RuntimeChainCatchAndFinallyDuplicateCallbacksSuppressed(t *testing.T) {
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
	var finallyCount int
	b.Register("monitor:downsample", func(context.Context, bus.Context) error { return errors.New("boom") })

	chainID, err := b.Chain(
		bus.NewJob("monitor:downsample", nil),
	).Catch(func(context.Context, bus.ChainState, error) error {
		catchCount++
		return nil
	}).Finally(func(context.Context, bus.ChainState) error {
		finallyCount++
		return nil
	}).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected chain dispatch error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once after failed chain, got %d", catchCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once after failed chain, got %d", finallyCount)
	}

	for _, callbackKind := range []string{"chain_catch", "chain_finally"} {
		cbPayload := map[string]any{
			"schema_version": 1,
			"dispatch_id":    "dup-dispatch",
			"kind":           "callback",
			"job_id":         "dup-job-" + callbackKind,
			"chain_id":       chainID,
			"callback_kind":  callbackKind,
			"error":          "boom",
		}
		if callbackKind == "chain_finally" {
			delete(cbPayload, "error")
		}
		if err := q.DispatchJSON(context.Background(), bus.InternalCallbackJobTypeForIntegration(), cbPayload); err != nil {
			t.Fatalf("dispatch duplicate callback (%s): %v", callbackKind, err)
		}
	}

	if catchCount != 1 {
		t.Fatalf("expected catch to remain once after duplicate callbacks, got %d", catchCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally to remain once after duplicate callbacks, got %d", finallyCount)
	}
}

func TestSQLStore_RuntimeChainFinallyCallbackReplayAfterDispatchFaultSuppressed(t *testing.T) {
	q := newFailFirstCallbackKindRuntime("chain_finally")
	store := newSQLiteStoreForRuntime(t)

	b, err := bus.NewWithStore(q, store)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })

	var finallyCount atomic.Int32
	chainID, err := b.Chain(
		bus.NewJob("monitor:poll", nil),
	).Finally(func(context.Context, bus.ChainState) error {
		finallyCount.Add(1)
		return nil
	}).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := b.FindChain(context.Background(), chainID)
		if err == nil && st.Completed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, err := b.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !st.Completed {
		t.Fatalf("expected completed chain state, got %+v", st)
	}
	if !q.failedOnce.Load() {
		t.Fatal("expected injected chain_finally callback dispatch failure")
	}
	if got := finallyCount.Load(); got != 0 {
		t.Fatalf("expected finally callback not invoked before replay, got %d", got)
	}

	cbPayload := map[string]any{
		"schema_version": 1,
		"dispatch_id":    st.DispatchID,
		"kind":           "callback",
		"job_id":         "replay-job-chain-finally",
		"chain_id":       chainID,
		"callback_kind":  "chain_finally",
	}
	if err := q.DispatchJSON(context.Background(), bus.InternalCallbackJobTypeForIntegration(), cbPayload); err != nil {
		t.Fatalf("dispatch replay callback first: %v", err)
	}
	if err := q.DispatchJSON(context.Background(), bus.InternalCallbackJobTypeForIntegration(), cbPayload); err != nil {
		t.Fatalf("dispatch replay callback duplicate: %v", err)
	}
	if got := finallyCount.Load(); got != 1 {
		t.Fatalf("expected finally callback once after replay + duplicate, got %d", got)
	}
}
