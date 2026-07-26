//go:build integration

package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/workflow"
	_ "modernc.org/sqlite"
)

// reliabilityInboundJob exposes one encoded workflow delivery to the internal handler.
type reliabilityInboundJob struct {
	payload []byte
}

// Bind decodes the workflow delivery into the requested envelope.
func (j reliabilityInboundJob) Bind(dst any) error {
	return json.Unmarshal(j.payload, dst)
}

// PayloadBytes returns an isolated copy of the encoded workflow delivery.
func (j reliabilityInboundJob) PayloadBytes() []byte {
	return append([]byte(nil), j.payload...)
}

// reliabilityRuntime synchronously executes physical workflow deliveries and supports deterministic dispatch faults.
type reliabilityRuntime struct {
	handlers         map[string]busruntime.Handler
	dispatches       atomic.Int32
	failDispatch     int32
	failCallbackKind string
	failedCallback   atomic.Bool
	failErr          error
}

// newReliabilityRuntime creates an isolated synchronous runtime for one reliability contract.
func newReliabilityRuntime() *reliabilityRuntime {
	return &reliabilityRuntime{
		handlers: make(map[string]busruntime.Handler),
		failErr:  errors.New("injected dispatch failure"),
	}
}

// Driver identifies the deterministic runtime used by these package-local tests.
func (r *reliabilityRuntime) Driver() Driver {
	return DriverSync
}

// WithContext returns the same synchronous runtime because deliveries receive their context directly.
func (r *reliabilityRuntime) WithContext(context.Context) queueRuntime {
	return r
}

// Dispatch rejects the legacy typed path because reliability contracts use workflow deliveries.
func (r *reliabilityRuntime) Dispatch(any) error {
	return errors.New("typed dispatch is not supported by the reliability runtime")
}

// Register is unused because the workflow engine owns logical handler registration.
func (r *reliabilityRuntime) Register(string, Handler) {}

// StartWorkers is inert because physical deliveries execute synchronously.
func (r *reliabilityRuntime) StartWorkers(context.Context) error {
	return nil
}

// Workers is inert because physical deliveries execute synchronously.
func (r *reliabilityRuntime) Workers(int) queueRuntime {
	return r
}

// Shutdown is inert because the reliability runtime owns no external resources.
func (r *reliabilityRuntime) Shutdown(context.Context) error {
	return nil
}

// Ready always succeeds because the reliability runtime has no external dependency.
func (r *reliabilityRuntime) Ready(context.Context) error {
	return nil
}

// physicalQueueNameOrDefault preserves the logical queue name for assertions.
func (r *reliabilityRuntime) physicalQueueNameOrDefault(queueName string) string {
	if queueName == "" {
		return "default"
	}
	return queueName
}

// setHandlerContextDecorator is inert because the workflow engine invokes logical handlers.
func (r *reliabilityRuntime) setHandlerContextDecorator(func(context.Context) context.Context) {}

// BusRegister records the private workflow handler for synchronous delivery.
func (r *reliabilityRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	r.handlers[jobType] = handler
}

// BusDispatch applies the configured fault before invoking the matching workflow handler.
func (r *reliabilityRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, _ busruntime.JobOptions) error {
	if r.dispatches.Add(1) == r.failDispatch {
		return r.failErr
	}
	if jobType == workflow.CallbackDeliveryType && r.failCallbackKind != "" && !r.failedCallback.Load() {
		var envelope struct {
			CallbackKind string `json:"callback_kind"`
		}
		if err := json.Unmarshal(payload, &envelope); err == nil && envelope.CallbackKind == r.failCallbackKind {
			r.failedCallback.Store(true)
			return r.failErr
		}
	}
	handler := r.handlers[jobType]
	if handler == nil {
		return fmt.Errorf("handler not registered for %q", jobType)
	}
	return handler(ctx, reliabilityInboundJob{payload: append([]byte(nil), payload...)})
}

// dispatchJSON encodes one literal private delivery for replay and duplicate assertions.
func (r *reliabilityRuntime) dispatchJSON(ctx context.Context, jobType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.BusDispatch(ctx, jobType, encoded, busruntime.JobOptions{})
}

// newReliabilityQueue creates a root Queue with a real SQLite workflow store.
func newReliabilityQueue(t *testing.T, runtime *reliabilityRuntime) *Queue {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "workflow-reliability.db"))
	if err != nil {
		t.Fatalf("open workflow database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLStore(SQLStoreConfig{
		DB:          db,
		DriverName:  "sqlite",
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new workflow store: %v", err)
	}
	q, err := newQueueFromRuntime(runtime, WithStore(store))
	if err != nil {
		t.Fatalf("new reliability queue: %v", err)
	}
	return q
}

// duplicateCallbackPayload creates a valid replay delivery for one workflow callback.
func duplicateCallbackPayload(workflowIDKey, workflowID, callbackKind string) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"dispatch_id":    "duplicate-dispatch",
		"kind":           "callback",
		"job_id":         "duplicate-" + callbackKind,
		workflowIDKey:    workflowID,
		"callback_kind":  callbackKind,
	}
}

// TestWorkflowReliability_SQLDuplicateCallbacksSuppressed proves persisted callback claims survive duplicate delivery.
func TestWorkflowReliability_SQLDuplicateCallbacksSuppressed(t *testing.T) {
	t.Run("batch then and finally", func(t *testing.T) {
		runtime := newReliabilityRuntime()
		q := newReliabilityQueue(t, runtime)
		var thenCount atomic.Int32
		var finallyCount atomic.Int32
		q.Register("reliability:batch:success", func(context.Context, Message) error { return nil })
		batchID, err := q.Batch(NewJob("reliability:batch:success")).
			Then(func(context.Context, BatchState) error {
				thenCount.Add(1)
				return nil
			}).
			Finally(func(context.Context, BatchState) error {
				finallyCount.Add(1)
				return nil
			}).
			Dispatch(context.Background())
		if err != nil {
			t.Fatalf("dispatch batch: %v", err)
		}
		for _, callbackKind := range []string{"batch_then", "batch_finally"} {
			if err := runtime.dispatchJSON(context.Background(), workflow.CallbackDeliveryType, duplicateCallbackPayload("batch_id", batchID, callbackKind)); err != nil {
				t.Fatalf("dispatch duplicate %s callback: %v", callbackKind, err)
			}
		}
		if thenCount.Load() != 1 || finallyCount.Load() != 1 {
			t.Fatalf("callback counts = then:%d finally:%d, want one each", thenCount.Load(), finallyCount.Load())
		}
	})

	t.Run("batch catch", func(t *testing.T) {
		runtime := newReliabilityRuntime()
		q := newReliabilityQueue(t, runtime)
		var catchCount atomic.Int32
		q.Register("reliability:batch:failure", func(context.Context, Message) error { return errors.New("boom") })
		batchID, err := q.Batch(NewJob("reliability:batch:failure")).
			Catch(func(context.Context, BatchState, error) error {
				catchCount.Add(1)
				return nil
			}).
			Dispatch(context.Background())
		if err == nil {
			t.Fatal("expected batch failure")
		}
		payload := duplicateCallbackPayload("batch_id", batchID, "batch_catch")
		payload["error"] = "boom"
		if err := runtime.dispatchJSON(context.Background(), workflow.CallbackDeliveryType, payload); err != nil {
			t.Fatalf("dispatch duplicate batch catch: %v", err)
		}
		if catchCount.Load() != 1 {
			t.Fatalf("batch catch count = %d, want 1", catchCount.Load())
		}
	})

	t.Run("chain finally", func(t *testing.T) {
		runtime := newReliabilityRuntime()
		q := newReliabilityQueue(t, runtime)
		var finallyCount atomic.Int32
		q.Register("reliability:chain:success", func(context.Context, Message) error { return nil })
		chainID, err := q.Chain(NewJob("reliability:chain:success")).
			Finally(func(context.Context, ChainState) error {
				finallyCount.Add(1)
				return nil
			}).
			Dispatch(context.Background())
		if err != nil {
			t.Fatalf("dispatch chain: %v", err)
		}
		if err := runtime.dispatchJSON(context.Background(), workflow.CallbackDeliveryType, duplicateCallbackPayload("chain_id", chainID, "chain_finally")); err != nil {
			t.Fatalf("dispatch duplicate chain finally: %v", err)
		}
		if finallyCount.Load() != 1 {
			t.Fatalf("chain finally count = %d, want 1", finallyCount.Load())
		}
	})

	t.Run("chain catch and finally", func(t *testing.T) {
		runtime := newReliabilityRuntime()
		q := newReliabilityQueue(t, runtime)
		var catchCount atomic.Int32
		var finallyCount atomic.Int32
		q.Register("reliability:chain:failure", func(context.Context, Message) error { return errors.New("boom") })
		chainID, err := q.Chain(NewJob("reliability:chain:failure")).
			Catch(func(context.Context, ChainState, error) error {
				catchCount.Add(1)
				return nil
			}).
			Finally(func(context.Context, ChainState) error {
				finallyCount.Add(1)
				return nil
			}).
			Dispatch(context.Background())
		if err == nil {
			t.Fatal("expected chain failure")
		}
		for _, callbackKind := range []string{"chain_catch", "chain_finally"} {
			payload := duplicateCallbackPayload("chain_id", chainID, callbackKind)
			if callbackKind == "chain_catch" {
				payload["error"] = "boom"
			}
			if err := runtime.dispatchJSON(context.Background(), workflow.CallbackDeliveryType, payload); err != nil {
				t.Fatalf("dispatch duplicate %s callback: %v", callbackKind, err)
			}
		}
		if catchCount.Load() != 1 || finallyCount.Load() != 1 {
			t.Fatalf("callback counts = catch:%d finally:%d, want one each", catchCount.Load(), finallyCount.Load())
		}
	})
}

// TestWorkflowReliability_SQLCallbackReplayAfterDispatchFault proves a failed callback delivery remains replayable exactly once.
func TestWorkflowReliability_SQLCallbackReplayAfterDispatchFault(t *testing.T) {
	runtime := newReliabilityRuntime()
	runtime.failCallbackKind = "chain_finally"
	runtime.failErr = errors.New("injected callback dispatch failure")
	q := newReliabilityQueue(t, runtime)
	q.Register("reliability:callback:replay", func(context.Context, Message) error { return nil })
	var finallyCount atomic.Int32
	chainID, err := q.Chain(NewJob("reliability:callback:replay")).
		Finally(func(context.Context, ChainState) error {
			finallyCount.Add(1)
			return nil
		}).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	state, err := q.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if !state.Completed || !runtime.failedCallback.Load() || finallyCount.Load() != 0 {
		t.Fatalf("pre-replay state = %+v, callback fault:%t count:%d", state, runtime.failedCallback.Load(), finallyCount.Load())
	}
	payload := duplicateCallbackPayload("chain_id", chainID, "chain_finally")
	payload["dispatch_id"] = state.DispatchID
	for range 2 {
		if err := runtime.dispatchJSON(context.Background(), workflow.CallbackDeliveryType, payload); err != nil {
			t.Fatalf("dispatch replay callback: %v", err)
		}
	}
	if finallyCount.Load() != 1 {
		t.Fatalf("finally count = %d, want 1 after replay and duplicate", finallyCount.Load())
	}
}

// TestWorkflowReliability_SQLInitialChainDispatchFailureStateConsistent proves admission failures persist terminal state and callbacks.
func TestWorkflowReliability_SQLInitialChainDispatchFailureStateConsistent(t *testing.T) {
	runtime := newReliabilityRuntime()
	runtime.failDispatch = 1
	runtime.failErr = errors.New("chain enqueue failed")
	q := newReliabilityQueue(t, runtime)
	var catchCount atomic.Int32
	var finallyCount atomic.Int32
	chainID, err := q.Chain(NewJob("reliability:chain:dispatch-failure")).
		Catch(func(context.Context, ChainState, error) error {
			catchCount.Add(1)
			return nil
		}).
		Finally(func(context.Context, ChainState) error {
			finallyCount.Add(1)
			return nil
		}).
		Dispatch(context.Background())
	if err == nil || chainID == "" {
		t.Fatalf("dispatch result = id:%q err:%v, want ID and error", chainID, err)
	}
	state, findErr := q.FindChain(context.Background(), chainID)
	if findErr != nil {
		t.Fatalf("find chain: %v", findErr)
	}
	if !state.Failed || state.Failure == "" || catchCount.Load() != 1 || finallyCount.Load() != 1 {
		t.Fatalf("failed chain state = %+v, callbacks catch:%d finally:%d", state, catchCount.Load(), finallyCount.Load())
	}
}

// TestWorkflowReliability_SQLPartialBatchDispatchFailureStateConsistent proves accepted work remains visible after later enqueue failure.
func TestWorkflowReliability_SQLPartialBatchDispatchFailureStateConsistent(t *testing.T) {
	runtime := newReliabilityRuntime()
	runtime.failDispatch = 2
	runtime.failErr = errors.New("batch enqueue failed after first job")
	q := newReliabilityQueue(t, runtime)
	q.Register("reliability:batch:partial", func(context.Context, Message) error { return nil })
	var catchCount atomic.Int32
	var finallyCount atomic.Int32
	batchID, err := q.Batch(
		NewJob("reliability:batch:partial"),
		NewJob("reliability:batch:partial"),
	).Catch(func(context.Context, BatchState, error) error {
		catchCount.Add(1)
		return nil
	}).Finally(func(context.Context, BatchState) error {
		finallyCount.Add(1)
		return nil
	}).Dispatch(context.Background())
	if err == nil || batchID == "" {
		t.Fatalf("dispatch result = id:%q err:%v, want ID and error", batchID, err)
	}
	state, findErr := q.FindBatch(context.Background(), batchID)
	if findErr != nil {
		t.Fatalf("find batch: %v", findErr)
	}
	if state.Completed || state.Cancelled || state.Processed != 1 || state.Pending != 1 || state.Failed != 0 {
		t.Fatalf("partial batch state = %+v, want processed=1 pending=1", state)
	}
	if catchCount.Load() != 0 || finallyCount.Load() != 0 {
		t.Fatalf("callback counts = catch:%d finally:%d, want zero", catchCount.Load(), finallyCount.Load())
	}
}
