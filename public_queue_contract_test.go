package queue_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
)

// TestPublicQueueContractDirectDispatchPreservesMessageIdentity verifies that the normal facade exposes one application job identity rather than its internal workflow envelope.
func TestPublicQueueContractDirectDispatchPreservesMessageIdentity(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	type payload struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	wantPayload := payload{ID: 42, Name: "facade"}
	var (
		seenMessage queue.Message
		seenPayload payload
	)
	q.Register("contract:direct", func(_ context.Context, message queue.Message) error {
		seenMessage = message
		return message.Bind(&seenPayload)
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	result, err := q.Dispatch(
		queue.NewJob("contract:direct").
			Payload(wantPayload).
			OnQueue("default"),
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.DispatchID == "" {
		t.Fatal("dispatch result must contain an ID")
	}
	if seenPayload != wantPayload {
		t.Fatalf("handler payload = %+v, want %+v", seenPayload, wantPayload)
	}
	if seenMessage.SchemaVersion == 0 {
		t.Fatal("message schema version must be populated")
	}
	if seenMessage.JobType != "contract:direct" {
		t.Fatalf("message job type = %q, want %q", seenMessage.JobType, "contract:direct")
	}
	if seenMessage.DispatchID != result.DispatchID {
		t.Fatalf("message dispatch ID = %q, want %q", seenMessage.DispatchID, result.DispatchID)
	}
	if seenMessage.JobID == "" {
		t.Fatal("message job ID must be populated")
	}
	if seenMessage.ChainID != "" || seenMessage.BatchID != "" {
		t.Fatalf("direct message unexpectedly contains workflow identity: %+v", seenMessage)
	}
}

// TestPublicQueueContractChainUsesCanonicalJobs verifies sequential execution, correlation, callbacks, and lookup through Queue alone.
func TestPublicQueueContractChainUsesCanonicalJobs(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	var messages []queue.Message
	q.Register("contract:chain:first", func(_ context.Context, message queue.Message) error {
		messages = append(messages, message)
		return nil
	})
	q.Register("contract:chain:second", func(_ context.Context, message queue.Message) error {
		messages = append(messages, message)
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var (
		finallyCalls int
		finallyState queue.ChainState
	)
	chainID, err := q.Chain(
		queue.NewJob("contract:chain:first"),
		queue.NewJob("contract:chain:second"),
	).
		OnQueue("critical").
		Finally(func(_ context.Context, state queue.ChainState) error {
			finallyCalls++
			finallyState = state
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
	if state.ChainID != chainID || state.DispatchID == "" {
		t.Fatalf("chain identity is incomplete: %+v", state)
	}
	if state.Queue != "critical" {
		t.Fatalf("chain queue = %q, want %q", state.Queue, "critical")
	}
	if !state.Completed || state.Failed || state.NextIndex != 2 {
		t.Fatalf("chain terminal state is inconsistent: %+v", state)
	}
	if len(messages) != 2 {
		t.Fatalf("chain handler calls = %d, want 2", len(messages))
	}
	wantTypes := []string{"contract:chain:first", "contract:chain:second"}
	for index, message := range messages {
		if message.JobType != wantTypes[index] {
			t.Fatalf("chain message %d job type = %q, want %q", index, message.JobType, wantTypes[index])
		}
		if message.ChainID != chainID || message.BatchID != "" {
			t.Fatalf("chain message %d has incorrect workflow identity: %+v", index, message)
		}
		if message.DispatchID != state.DispatchID || message.JobID == "" {
			t.Fatalf("chain message %d has incomplete correlation: %+v", index, message)
		}
	}
	if messages[0].JobID == messages[1].JobID {
		t.Fatalf("chain nodes share job ID %q", messages[0].JobID)
	}
	if finallyCalls != 1 || finallyState.ChainID != chainID || !finallyState.Completed {
		t.Fatalf("chain finally callback = (%d, %+v), want one completed callback", finallyCalls, finallyState)
	}
}

// TestPublicQueueContractBatchUsesCanonicalJobs verifies aggregate state, correlation, and callback behavior through Queue alone.
func TestPublicQueueContractBatchUsesCanonicalJobs(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	var messages []queue.Message
	q.Register("contract:batch:item", func(_ context.Context, message queue.Message) error {
		messages = append(messages, message)
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	var (
		progressCalls int
		thenCalls     int
		finallyCalls  int
	)
	batchID, err := q.Batch(
		queue.NewJob("contract:batch:item").Payload(map[string]int{"id": 1}),
		queue.NewJob("contract:batch:item").Payload(map[string]int{"id": 2}),
	).
		Name("public contract").
		OnQueue("bulk").
		Progress(func(_ context.Context, _ queue.BatchState) error {
			progressCalls++
			return nil
		}).
		Then(func(_ context.Context, _ queue.BatchState) error {
			thenCalls++
			return nil
		}).
		Finally(func(_ context.Context, _ queue.BatchState) error {
			finallyCalls++
			return nil
		}).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}

	state, err := q.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if state.BatchID != batchID || state.DispatchID == "" {
		t.Fatalf("batch identity is incomplete: %+v", state)
	}
	if state.Name != "public contract" || state.Queue != "bulk" {
		t.Fatalf("batch metadata is incomplete: %+v", state)
	}
	if !state.Completed || state.Cancelled || state.Total != 2 || state.Processed != 2 || state.Pending != 0 || state.Failed != 0 {
		t.Fatalf("batch terminal state is inconsistent: %+v", state)
	}
	if len(messages) != 2 {
		t.Fatalf("batch handler calls = %d, want 2", len(messages))
	}
	for index, message := range messages {
		if message.JobType != "contract:batch:item" {
			t.Fatalf("batch message %d job type = %q, want %q", index, message.JobType, "contract:batch:item")
		}
		if message.BatchID != batchID || message.ChainID != "" {
			t.Fatalf("batch message %d has incorrect workflow identity: %+v", index, message)
		}
		if message.DispatchID != state.DispatchID || message.JobID == "" {
			t.Fatalf("batch message %d has incomplete correlation: %+v", index, message)
		}
	}
	if messages[0].JobID == messages[1].JobID {
		t.Fatalf("batch items share job ID %q", messages[0].JobID)
	}
	if progressCalls != 2 || thenCalls != 1 || finallyCalls != 1 {
		t.Fatalf("batch callback calls = progress:%d then:%d finally:%d, want 2/1/1", progressCalls, thenCalls, finallyCalls)
	}
}

// TestPublicQueueContractRetryEventuallySucceeds verifies retry policy remains effective when dispatched through the workflow-capable facade.
func TestPublicQueueContractRetryEventuallySucceeds(t *testing.T) {
	var (
		attempts atomic.Int32
		messages []queue.Message
	)
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})
	q.Register("contract:retry", func(_ context.Context, message queue.Message) error {
		messages = append(messages, message)
		if attempts.Add(1) < 3 {
			return errors.New("transient contract failure")
		}
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	result, err := q.Dispatch(queue.NewJob("contract:retry").OnQueue("default").Retry(2))
	if err != nil {
		t.Fatalf("dispatch retrying job: %v", err)
	}
	if attempts.Load() != 3 || len(messages) != 3 {
		t.Fatalf("retry handler calls = %d/%d, want 3", attempts.Load(), len(messages))
	}
	for index, message := range messages {
		if message.DispatchID != result.DispatchID || message.JobID == "" {
			t.Fatalf("retry message %d has incomplete identity: %+v", index, message)
		}
		if message.JobID != messages[0].JobID {
			t.Fatalf("retry message %d job ID = %q, want stable ID %q", index, message.JobID, messages[0].JobID)
		}
	}
}

// TestPublicQueueContractUniqueValidationReachesFacade verifies invalid deduplication policy cannot be hidden by the internal workflow envelope.
func TestPublicQueueContractUniqueValidationReachesFacade(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	var calls atomic.Int32
	q.Register("contract:unique", func(context.Context, queue.Message) error {
		calls.Add(1)
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	if _, err := q.Dispatch(queue.NewJob("contract:unique").UniqueFor(-time.Second)); err == nil {
		t.Fatal("negative uniqueness TTL must fail public dispatch")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid unique job executed %d times", calls.Load())
	}
}

// TestPublicQueueContractObserverSpansEveryLayer verifies one exported observer receives queue, worker, and workflow facts.
func TestPublicQueueContractObserverSpansEveryLayer(t *testing.T) {
	var (
		eventsMu sync.Mutex
		events   []queue.Event
	)
	observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})
	q, err := queue.NewSync(queue.WithObserver(observer))
	if err != nil {
		t.Fatalf("new observed sync queue: %v", err)
	}
	q.Register("contract:observed", func(context.Context, queue.Message) error { return nil })
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	result, err := q.Dispatch(queue.NewJob("contract:observed").OnQueue("default"))
	if err != nil {
		t.Fatalf("dispatch observed job: %v", err)
	}
	eventsMu.Lock()
	snapshot := append([]queue.Event(nil), events...)
	eventsMu.Unlock()

	required := map[queue.EventKind]queue.EventLayer{
		queue.EventDispatchStarted:   queue.EventLayerQueue,
		queue.EventEnqueueAccepted:   queue.EventLayerQueue,
		queue.EventProcessStarted:    queue.EventLayerWorker,
		queue.EventJobStarted:        queue.EventLayerWorkflow,
		queue.EventProcessSucceeded:  queue.EventLayerWorker,
		queue.EventJobSucceeded:      queue.EventLayerWorkflow,
		queue.EventDispatchSucceeded: queue.EventLayerQueue,
	}
	var correlatedJobID string
	for kind, wantLayer := range required {
		var found *queue.Event
		for index := range snapshot {
			if snapshot[index].Kind == kind {
				found = &snapshot[index]
				break
			}
		}
		if found == nil {
			t.Errorf("observer did not receive %q: %+v", kind, snapshot)
			continue
		}
		if found.Layer != wantLayer {
			t.Errorf("%q layer = %q, want %q", kind, found.Layer, wantLayer)
		}
		if found.SchemaVersion == 0 || found.EventID == "" || found.Time.IsZero() {
			t.Errorf("%q event envelope is incomplete: %+v", kind, *found)
		}
		if found.JobType != "contract:observed" {
			t.Errorf("%q job type = %q, want %q", kind, found.JobType, "contract:observed")
		}
		if found.DispatchID != result.DispatchID {
			t.Errorf("%q dispatch ID = %q, want %q", kind, found.DispatchID, result.DispatchID)
		}
		if found.JobID == "" {
			t.Errorf("%q job ID is empty", kind)
		} else if correlatedJobID == "" {
			correlatedJobID = found.JobID
		} else if found.JobID != correlatedJobID {
			t.Errorf("%q job ID = %q, want shared ID %q", kind, found.JobID, correlatedJobID)
		}
	}
}

// TestPublicQueueContractLookupUsesOneNotFoundError verifies both workflow shapes share the root lookup error contract.
func TestPublicQueueContractLookupUsesOneNotFoundError(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	if _, err := q.FindChain(context.Background(), "missing-chain"); !errors.Is(err, queue.ErrWorkflowNotFound) {
		t.Fatalf("find missing chain error = %v, want ErrWorkflowNotFound", err)
	}
	if _, err := q.FindBatch(context.Background(), "missing-batch"); !errors.Is(err, queue.ErrWorkflowNotFound) {
		t.Fatalf("find missing batch error = %v, want ErrWorkflowNotFound", err)
	}
}

// TestPublicQueueContractContextHandlesShareLifecycle verifies derived handles do not fork registration, worker, or shutdown state.
func TestPublicQueueContractContextHandlesShareLifecycle(t *testing.T) {
	type contextKey string
	const markerKey contextKey = "public-contract-marker"

	q, err := queue.NewWorkerpool(queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new workerpool queue: %v", err)
	}
	derived := q.WithContext(context.WithValue(context.Background(), markerKey, "derived"))
	t.Cleanup(func() {
		if shutdownErr := derived.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("derived shutdown: %v", shutdownErr)
		}
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("base shutdown: %v", shutdownErr)
		}
	})

	seen := make(chan string, 1)
	q.Register("contract:lifecycle", func(ctx context.Context, _ queue.Message) error {
		value, _ := ctx.Value(markerKey).(string)
		seen <- value
		return nil
	})
	if err := derived.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers through derived handle: %v", err)
	}
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("idempotent start through base handle: %v", err)
	}
	if err := q.Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if _, err := derived.Dispatch(queue.NewJob("contract:lifecycle").OnQueue("default")); err != nil {
		t.Fatalf("dispatch through derived handle: %v", err)
	}
	select {
	case value := <-seen:
		if value != "derived" {
			t.Fatalf("handler context marker = %q, want %q", value, "derived")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workerpool handler did not run")
	}

	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown through base handle: %v", err)
	}
	if err := derived.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown through derived handle: %v", err)
	}
	if _, err := derived.Dispatch(queue.NewJob("contract:lifecycle").OnQueue("default")); !errors.Is(err, queue.ErrQueuerShuttingDown) {
		t.Fatalf("dispatch after shared shutdown error = %v, want ErrQueuerShuttingDown", err)
	}
}
