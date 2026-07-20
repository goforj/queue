package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

type failingDispatchQueue struct {
	err       error
	handlers  map[string]busruntime.Handler
	workerCnt int
}

func (q *failingDispatchQueue) StartWorkers(context.Context) error { return nil }
func (q *failingDispatchQueue) Shutdown(context.Context) error     { return nil }

func (q *failingDispatchQueue) BusRegister(jobType string, handler busruntime.Handler) {
	if q.handlers == nil {
		q.handlers = make(map[string]busruntime.Handler)
	}
	q.handlers[jobType] = handler
}

func (q *failingDispatchQueue) BusDispatch(context.Context, string, []byte, busruntime.JobOptions) error {
	return q.err
}

func TestDispatchEnqueueFailureEmitsStartedThenFailed(t *testing.T) {
	q := &failingDispatchQueue{err: errors.New("enqueue failed")}
	var kinds []EventKind
	b, err := NewWithStore(q, NewMemoryStore(), WithObserver(ObserverFunc(func(_ context.Context, e Event) {
		kinds = append(kinds, e.Kind)
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	res, err := b.Dispatch(context.Background(), NewJob("monitor:poll", nil))
	if err == nil {
		t.Fatal("expected dispatch enqueue failure")
	}
	if res.DispatchID == "" {
		t.Fatal("expected non-empty dispatch id on enqueue failure")
	}
	if len(kinds) != 2 {
		t.Fatalf("expected 2 events, got %d (%v)", len(kinds), kinds)
	}
	if kinds[0] != EventDispatchStarted || kinds[1] != EventDispatchFailed {
		t.Fatalf("expected started then failed, got %v", kinds)
	}
}

func TestUnknownCallbackKindEmitsCallbackFailed(t *testing.T) {
	q := newSyncTestRuntime()
	var started int
	var failed int
	b, err := New(q, WithObserver(ObserverFunc(func(_ context.Context, e Event) {
		if e.Kind == EventCallbackStarted {
			started++
		}
		if e.Kind == EventCallbackFailed {
			failed++
		}
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	payload := map[string]any{
		"schema_version": 1,
		"dispatch_id":    "d1",
		"kind":           "callback",
		"job_id":         "j1",
		"callback_kind":  "unknown_kind",
	}
	if err := q.DispatchJSON(context.Background(), internalJobCallback, payload); err == nil {
		t.Fatal("expected unknown callback kind error")
	}
	if started != 0 {
		t.Fatalf("invalid callback emitted %d started events, want 0", started)
	}
	if failed != 1 {
		t.Fatalf("expected callback failed once, got %d", failed)
	}
}

func TestCallbackMissingRequiredIDsEmitsCallbackFailed(t *testing.T) {
	q := newSyncTestRuntime()
	var failed int
	b, err := New(q, WithObserver(ObserverFunc(func(_ context.Context, e Event) {
		if e.Kind == EventCallbackFailed {
			failed++
		}
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	tests := []map[string]any{
		{
			"schema_version": 1,
			"dispatch_id":    "d1",
			"kind":           "callback",
			"job_id":         "j1",
			"callback_kind":  "chain_catch",
			// missing chain_id
		},
		{
			"schema_version": 1,
			"dispatch_id":    "d2",
			"kind":           "callback",
			"job_id":         "j2",
			"callback_kind":  "batch_then",
			// missing batch_id
		},
	}

	for i, payloadMap := range tests {
		if err := q.DispatchJSON(context.Background(), internalJobCallback, payloadMap); err == nil {
			t.Fatalf("expected callback validation error for case %d", i)
		}
	}

	if failed != len(tests) {
		t.Fatalf("expected %d callback failed events, got %d", len(tests), failed)
	}
}

// TestCallbackFunctionErrorEmitsFailed verifies an invoked ephemeral callback cannot be reported as successful.
func TestCallbackFunctionErrorEmitsFailed(t *testing.T) {
	tests := []struct {
		name       string
		handlerErr error
		dispatch   func(Engine, error)
	}{
		{
			name:       "chain catch",
			handlerErr: errors.New("handler failed"),
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Chain(NewJob("job:callback-error", nil)).
					Catch(func(context.Context, ChainState, error) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name: "chain finally",
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Chain(NewJob("job:callback-error", nil)).
					Finally(func(context.Context, ChainState) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name:       "batch catch",
			handlerErr: errors.New("handler failed"),
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Batch(NewJob("job:callback-error", nil)).
					Catch(func(context.Context, BatchState, error) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name: "batch then",
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Batch(NewJob("job:callback-error", nil)).
					Then(func(context.Context, BatchState) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
		{
			name: "batch finally",
			dispatch: func(b Engine, callbackErr error) {
				_, _ = b.Batch(NewJob("job:callback-error", nil)).
					Finally(func(context.Context, BatchState) error { return callbackErr }).
					Dispatch(context.Background())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := newSyncTestRuntime()
			callbackErr := errors.New("callback failed")
			var (
				failed    []Event
				succeeded []Event
			)
			b, err := New(q, WithObserver(ObserverFunc(func(_ context.Context, event Event) {
				switch event.Kind {
				case EventCallbackFailed:
					failed = append(failed, event)
				case EventCallbackSucceeded:
					succeeded = append(succeeded, event)
				}
			})))
			if err != nil {
				t.Fatalf("new bus: %v", err)
			}
			b.Register("job:callback-error", func(context.Context, Context) error { return test.handlerErr })
			if err := b.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start workers: %v", err)
			}
			test.dispatch(b, callbackErr)
			if len(failed) != 1 || !errors.Is(failed[0].Err, callbackErr) {
				t.Fatalf("callback failed events = %#v, want callback cause", failed)
			}
			for _, event := range succeeded {
				if event.JobID == failed[0].JobID {
					t.Fatalf("failed callback job %q later emitted success", event.JobID)
				}
			}
		})
	}
}

// TestCallbackPanicEmitsFailed verifies callback recovery preserves a terminal lifecycle fact and the panic cause.
func TestCallbackPanicEmitsFailed(t *testing.T) {
	queueRuntime := newSyncTestRuntime()
	panicErr := errors.New("callback panic")
	var started int
	var failed []Event
	var succeeded int
	busRuntime, err := New(queueRuntime, WithObserver(ObserverFunc(func(_ context.Context, event Event) {
		switch event.Kind {
		case EventCallbackStarted:
			started++
		case EventCallbackFailed:
			failed = append(failed, event)
		case EventCallbackSucceeded:
			succeeded++
		}
	})))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	busRuntime.Register("job:callback-panic", func(context.Context, Context) error { return nil })
	if err := busRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	if _, err := busRuntime.Batch(NewJob("job:callback-panic", nil)).
		Then(func(context.Context, BatchState) error { panic(panicErr) }).
		Dispatch(context.Background()); err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	if started != 1 || len(failed) != 1 || succeeded != 0 {
		t.Fatalf("callback started/failed/succeeded = %d/%d/%d, want 1/1/0", started, len(failed), succeeded)
	}
	if !errors.Is(failed[0].Err, panicErr) {
		t.Fatalf("callback panic event error = %v, want cause %v", failed[0].Err, panicErr)
	}
}

// TestPositiveWorkflowEventsWaitForDeliverySettlement verifies broker-backed success facts remain pending until acknowledgement.
func TestPositiveWorkflowEventsWaitForDeliverySettlement(t *testing.T) {
	positive := []EventKind{
		EventJobSucceeded,
		EventChainAdvanced,
		EventChainCompleted,
		EventBatchProgressed,
		EventBatchCompleted,
		EventCallbackSucceeded,
	}
	for _, kind := range positive {
		t.Run(string(kind), func(t *testing.T) {
			var events []Event
			runtime := &runtime{observer: ObserverFunc(func(_ context.Context, event Event) {
				events = append(events, event)
			})}
			ctx, settlement := busruntime.WithDeliverySettlement(context.Background())
			runtime.emit(ctx, Event{Kind: kind})
			if len(events) != 0 {
				t.Fatalf("event %q emitted before settlement: %+v", kind, events)
			}
			settlement.Commit()
			if len(events) != 1 || events[0].Kind != kind {
				t.Fatalf("events after settlement = %+v, want %q", events, kind)
			}
		})
	}
}

// TestStableWorkflowFactIDPreservesLogicalIdentity verifies replay identifiers
// are deterministic, kind-specific, and immune to ambiguous field partitioning.
func TestStableWorkflowFactIDPreservesLogicalIdentity(t *testing.T) {
	want := stableWorkflowFactID(EventChainCompleted, "chain", "node")
	if len(want) != len("evt_")+32 {
		t.Fatalf("stable fact id length = %d, want %d", len(want), len("evt_")+32)
	}
	if got := stableWorkflowFactID(EventChainCompleted, "chain", "node"); got != want {
		t.Fatalf("replayed fact id = %q, want %q", got, want)
	}
	if got := stableWorkflowFactID(EventChainAdvanced, "chain", "node"); got == want {
		t.Fatalf("different event kinds share fact id %q", got)
	}
	if left, right := stableWorkflowFactID(EventJobSucceeded, "ab", "c"), stableWorkflowFactID(EventJobSucceeded, "a", "bc"); left == right {
		t.Fatalf("differently framed identities share fact id %q", left)
	}
}

// TestStoredJobOutcomeFactIDRequiresCorrelation keeps unrelated legacy
// deliveries from sharing a deterministic identifier merely by type/attempt.
func TestStoredJobOutcomeFactIDRequiresCorrelation(t *testing.T) {
	correlated := storedJobOutcome{env: envelope{
		DispatchID: "dispatch-fact-id",
		JobID:      "job-fact-id",
		Job: StoredJob{
			Type:    "workflow:fact-id",
			Payload: []byte(`{"version":1}`),
			Options: JobOptions{Queue: "critical"},
		},
		Attempt: 2,
	}}
	first := storedJobOutcomeFactID(EventJobSucceeded, correlated)
	if second := storedJobOutcomeFactID(EventJobSucceeded, correlated); second != first {
		t.Fatalf("correlated fact ids = %q/%q, want stable", first, second)
	}
	for _, test := range []struct {
		name   string
		mutate func(*storedJobOutcome)
	}{
		{name: "payload", mutate: func(outcome *storedJobOutcome) { outcome.env.Job.Payload = []byte(`{"version":2}`) }},
		{name: "queue", mutate: func(outcome *storedJobOutcome) { outcome.env.Job.Options.Queue = "bulk" }},
		{name: "job type", mutate: func(outcome *storedJobOutcome) { outcome.env.Job.Type = "workflow:other-fact" }},
		{name: "attempt", mutate: func(outcome *storedJobOutcome) { outcome.env.Attempt++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := correlated
			test.mutate(&changed)
			if got := storedJobOutcomeFactID(EventJobSucceeded, changed); got == first {
				t.Fatalf("changed %s reused fact id %q", test.name, got)
			}
		})
	}

	uncorrelated := correlated
	uncorrelated.env.DispatchID = ""
	if left, right := storedJobOutcomeFactID(EventJobSucceeded, uncorrelated), storedJobOutcomeFactID(EventJobSucceeded, uncorrelated); left == right {
		t.Fatalf("uncorrelated delivery reused fact id %q", left)
	}
	uncorrelated = correlated
	uncorrelated.env.JobID = ""
	if left, right := storedJobOutcomeFactID(EventJobSucceeded, uncorrelated), storedJobOutcomeFactID(EventJobSucceeded, uncorrelated); left == right {
		t.Fatalf("partially correlated delivery reused fact id %q", left)
	}
	if left, right := storedJobOutcomeFactID(EventJobFailed, correlated), storedJobOutcomeFactID(EventJobFailed, correlated); left == right {
		t.Fatalf("non-recoverable failures reused fact id %q", left)
	}
}

// TestAggregateFactIDsIncludeObservableCorrelation proves deterministic chain
// and batch identifiers cannot label events whose visible job fields disagree.
func TestAggregateFactIDsIncludeObservableCorrelation(t *testing.T) {
	base := envelope{
		DispatchID: "dispatch-aggregate-fact-id",
		JobID:      "job-aggregate-fact-id",
		ChainID:    "chain-aggregate-fact-id",
		BatchID:    "batch-aggregate-fact-id",
		NodeID:     "node-aggregate-fact-id",
		Job: StoredJob{
			Type:    "workflow:aggregate-fact-id",
			Payload: []byte(`{"version":1}`),
			Options: JobOptions{Queue: "critical"},
		},
	}
	chainID := chainFactID(EventChainAdvanced, base)
	batchID := batchFactID(EventBatchProgressed, base)
	if got := chainFactID(EventChainAdvanced, base); got != chainID {
		t.Fatalf("replayed chain fact ids = %q/%q, want stable", chainID, got)
	}
	if got := batchFactID(EventBatchProgressed, base); got != batchID {
		t.Fatalf("replayed batch fact ids = %q/%q, want stable", batchID, got)
	}
	for _, test := range []struct {
		name   string
		mutate func(*envelope)
	}{
		{name: "dispatch", mutate: func(env *envelope) { env.DispatchID = "dispatch-other" }},
		{name: "job", mutate: func(env *envelope) { env.JobID = "job-other" }},
		{name: "job type", mutate: func(env *envelope) { env.Job.Type = "workflow:other-fact" }},
		{name: "payload", mutate: func(env *envelope) { env.Job.Payload = []byte(`{"version":2}`) }},
		{name: "queue", mutate: func(env *envelope) { env.Job.Options.Queue = "bulk" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			if got := chainFactID(EventChainAdvanced, changed); got == chainID {
				t.Fatalf("changed %s reused chain fact id %q", test.name, got)
			}
			if got := batchFactID(EventBatchProgressed, changed); got == batchID {
				t.Fatalf("changed %s reused batch fact id %q", test.name, got)
			}
		})
	}
}

// TestRecoveredSuccessRetainsLogicalIdentityWithoutReplayTiming proves recovery
// deduplicates the same application attempt without borrowing failed replay telemetry.
func TestRecoveredSuccessRetainsLogicalIdentityWithoutReplayTiming(t *testing.T) {
	started := time.Unix(10, 0)
	finished := time.Unix(12, 0)
	normal := storedJobOutcome{
		env: envelope{
			DispatchID: "dispatch-recovered-success-id",
			JobID:      "job-recovered-success-id",
			ChainID:    "chain-recovered-success-id",
			Attempt:    0,
			Job:        StoredJob{Type: "workflow:recovered-success-id"},
		},
		started:  started,
		finished: finished,
	}
	replayed := normal
	replayed.attempt = busruntime.DeliveryAttempt{Number: 0, MaxRetry: 1}
	replayed.started = time.Unix(20, 0)
	replayed.finished = time.Unix(25, 0)
	replayed.err = errors.New("contradictory replay failure")
	observed := time.Unix(30, 0)
	receipt := transitionReceipt{
		version:            transitionReceiptVersion,
		eventSchemaVersion: eventSchemaVersion,
		outcome:            BatchJobSucceeded,
		owner: transitionClaim{
			deliveryID:     "generation-recovered-success-id",
			attempt:        0,
			dispatchID:     normal.env.DispatchID,
			jobID:          normal.env.JobID,
			jobFingerprint: storedJobReceiptFingerprint(normal.env.Job),
		},
	}
	recovered, err := recoveredStoredJobSuccess(replayed, receipt, observed)
	if err != nil {
		t.Fatalf("recover stored job success: %v", err)
	}
	if got, want := storedJobOutcomeFactID(EventJobSucceeded, recovered), storedJobOutcomeFactID(EventJobSucceeded, normal); got != want {
		t.Fatalf("recovered/normal fact ids = %q/%q", got, want)
	}
	if recovered.err != nil || recovered.env.Attempt != 0 || recovered.attempt.Number != 0 || !recovered.started.Equal(observed) || !recovered.finished.Equal(observed) || recovered.finished.Sub(recovered.started) != 0 {
		t.Fatalf("recovered outcome retained replay telemetry: %+v", recovered)
	}
	nextAttempt := normal
	nextAttempt.env.Attempt++
	if got, previous := storedJobOutcomeFactID(EventJobSucceeded, nextAttempt), storedJobOutcomeFactID(EventJobSucceeded, normal); got == previous {
		t.Fatalf("different application attempts share fact id %q", got)
	}
}

// TestRecoveredSuccessRequiresValidTransitionReceipt rejects incomplete or
// mismatched durable identity before a reconstructed fact can be emitted.
func TestRecoveredSuccessRequiresValidTransitionReceipt(t *testing.T) {
	job := StoredJob{Type: "workflow:receipt-validation", Payload: []byte(`{"id":1}`)}
	outcome := storedJobOutcome{env: envelope{
		DispatchID: "dispatch-receipt-validation",
		JobID:      "job-receipt-validation",
		Attempt:    1,
		Job:        job,
	}}
	valid := transitionReceipt{
		version:            transitionReceiptVersion,
		eventSchemaVersion: eventSchemaVersion,
		outcome:            BatchJobSucceeded,
		owner: transitionClaim{
			deliveryID:     "generation-receipt-validation",
			attempt:        1,
			dispatchID:     outcome.env.DispatchID,
			jobID:          outcome.env.JobID,
			jobFingerprint: storedJobReceiptFingerprint(job),
		},
	}
	for _, test := range []struct {
		name   string
		mutate func(*transitionReceipt)
	}{
		{name: "failed outcome", mutate: func(receipt *transitionReceipt) { receipt.outcome = BatchJobFailed }},
		{name: "unknown receipt version", mutate: func(receipt *transitionReceipt) { receipt.version++ }},
		{name: "unknown event schema", mutate: func(receipt *transitionReceipt) { receipt.eventSchemaVersion++ }},
		{name: "empty delivery owner", mutate: func(receipt *transitionReceipt) { receipt.owner.deliveryID = "" }},
		{name: "negative attempt", mutate: func(receipt *transitionReceipt) { receipt.owner.attempt = -1 }},
		{name: "different attempt", mutate: func(receipt *transitionReceipt) { receipt.owner.attempt = 2 }},
		{name: "dispatch mismatch", mutate: func(receipt *transitionReceipt) { receipt.owner.dispatchID = "different" }},
		{name: "job mismatch", mutate: func(receipt *transitionReceipt) { receipt.owner.jobID = "different" }},
		{name: "fingerprint mismatch", mutate: func(receipt *transitionReceipt) { receipt.owner.jobFingerprint = "different" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := valid
			test.mutate(&receipt)
			if _, err := recoveredStoredJobSuccess(outcome, receipt, time.Now()); err == nil {
				t.Fatal("invalid transition receipt was accepted")
			}
		})
	}
}

// TestRecoveredTransitionReceiptLogicalValidationSeparatesPhysicalOwnership
// keeps malformed identity fail-closed while permitting legitimate nonowners.
func TestRecoveredTransitionReceiptLogicalValidationSeparatesPhysicalOwnership(t *testing.T) {
	job := StoredJob{Type: "workflow:logical-receipt-validation", Payload: []byte(`{"id":2}`)}
	env := envelope{
		DispatchID: "dispatch-logical-receipt-validation",
		JobID:      "job-logical-receipt-validation",
		Attempt:    2,
		Job:        job,
	}
	valid := transitionReceipt{
		version:            transitionReceiptVersion,
		eventSchemaVersion: eventSchemaVersion,
		owner: transitionClaim{
			deliveryID:     "generation-logical-receipt-validation",
			attempt:        env.Attempt,
			dispatchID:     env.DispatchID,
			jobID:          env.JobID,
			jobFingerprint: storedJobReceiptFingerprint(job),
		},
	}
	for _, test := range []struct {
		name              string
		requireOwnerJobID bool
		mutateEnv         func(*envelope)
		mutateReceipt     func(*transitionReceipt)
		wantErr           bool
	}{
		{name: "chain different owner attempt", mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.attempt++ }},
		{name: "chain different owner job", mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.jobID = "job-other-physical-delivery" }},
		{name: "batch different owner attempt", requireOwnerJobID: true, mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.attempt++ }},
		{name: "batch different logical member", requireOwnerJobID: true, mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.jobID = "job-other-member" }, wantErr: true},
		{name: "chain negative current attempt", mutateEnv: func(env *envelope) { env.Attempt = -1 }},
		{name: "empty delivery dispatch", mutateEnv: func(env *envelope) { env.DispatchID = "" }, wantErr: true},
		{name: "empty delivery job", mutateEnv: func(env *envelope) { env.JobID = "" }, wantErr: true},
		{name: "empty owner generation", mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.deliveryID = "" }, wantErr: true},
		{name: "negative owner attempt", mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.attempt = -1 }, wantErr: true},
		{name: "empty owner job", mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.jobID = "" }, wantErr: true},
		{name: "owner dispatch mismatch", mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.dispatchID = "dispatch-other" }, wantErr: true},
		{name: "owner fingerprint mismatch", mutateReceipt: func(receipt *transitionReceipt) { receipt.owner.jobFingerprint = "fingerprint-other" }, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			currentEnv := env
			currentReceipt := valid
			if test.mutateEnv != nil {
				test.mutateEnv(&currentEnv)
			}
			if test.mutateReceipt != nil {
				test.mutateReceipt(&currentReceipt)
			}
			err := validateRecoveredTransitionReceipt(currentEnv, currentReceipt, test.requireOwnerJobID)
			if (err != nil) != test.wantErr {
				t.Fatalf("logical receipt validation error = %v, want error %t", err, test.wantErr)
			}
		})
	}
}

// TestDuplicateFailedCallbackDoesNotBecomeSuccessful verifies an at-most-once callback marker cannot turn redelivery into a false success.
func TestDuplicateFailedCallbackDoesNotBecomeSuccessful(t *testing.T) {
	const batchID = "batch_callback_failed_duplicate"
	store := NewMemoryStore()
	if err := store.CreateBatch(context.Background(), BatchRecord{
		BatchID: batchID,
		Jobs:    []BatchJob{{JobID: "batch_callback_job", Job: StoredJob{Type: "callback:source"}}},
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if _, _, err := store.MarkBatchJobSucceeded(context.Background(), batchID, "batch_callback_job"); err != nil {
		t.Fatalf("complete batch: %v", err)
	}
	runtime, queueRuntime, recorder := newWorkflowMutationRuntime(t, store)
	callbackErr := errors.New("callback failed")
	runtime.batchCallbacks[batchID] = batchCallbacks{
		then: func(context.Context, BatchState) error { return callbackErr },
	}
	env := envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    "dispatch_callback_failed_duplicate",
		JobID:         "job_callback_failed_duplicate",
		BatchID:       batchID,
		CallbackKind:  "batch_then",
	}
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobCallback, env); !errors.Is(err, callbackErr) {
		t.Fatalf("first callback error = %v, want %v", err, callbackErr)
	}
	if err := queueRuntime.DispatchJSON(exhaustedWorkflowContext(), internalJobCallback, env); err != nil {
		t.Fatalf("duplicate callback delivery: %v", err)
	}
	failed := 0
	succeeded := 0
	for _, event := range recorder.events {
		switch event.Kind {
		case EventCallbackFailed:
			failed++
		case EventCallbackSucceeded:
			succeeded++
		}
	}
	if failed != 1 || succeeded != 0 {
		t.Fatalf("callback failed/succeeded events = %d/%d, want 1/0", failed, succeeded)
	}
}

func TestMultiObserverPanicsAreIsolated(t *testing.T) {
	var called int
	observer := MultiObserver(
		ObserverFunc(func(context.Context, Event) { panic("boom") }),
		ObserverFunc(func(context.Context, Event) { called++ }),
	)
	observer.Observe(context.Background(), Event{Kind: EventDispatchStarted})
	if called != 1 {
		t.Fatalf("expected second observer called once despite panic, got %d", called)
	}
}

func TestChainEnqueueFailureInvokesCatchAndFinally(t *testing.T) {
	q := &failingDispatchQueue{err: errors.New("enqueue failed")}
	bi, err := NewWithStore(q, NewMemoryStore())
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	b := bi.(*runtime)

	var catchCount int
	var finallyCount int
	chainID, err := b.Chain(NewJob("monitor:poll", nil)).
		Catch(func(context.Context, ChainState, error) error {
			catchCount++
			return nil
		}).
		Finally(func(context.Context, ChainState) error {
			finallyCount++
			return nil
		}).
		Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected chain enqueue error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once, got %d", catchCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}
	st, err := b.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find failed chain: %v", err)
	}
	if !st.Failed {
		t.Fatalf("expected chain marked failed, got %+v", st)
	}
	b.mu.RLock()
	cbCount := len(b.chainCallbacks)
	b.mu.RUnlock()
	if cbCount != 0 {
		t.Fatalf("expected chain callbacks cleaned, got %d", cbCount)
	}
}

func TestBatchEnqueueFailureInvokesCatchAndFinally(t *testing.T) {
	q := &failingDispatchQueue{err: errors.New("enqueue failed")}
	bi, err := NewWithStore(q, NewMemoryStore())
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	b := bi.(*runtime)

	var catchCount int
	var finallyCount int
	batchID, err := b.Batch(NewJob("monitor:poll", nil)).
		Catch(func(context.Context, BatchState, error) error {
			catchCount++
			return nil
		}).
		Finally(func(context.Context, BatchState) error {
			finallyCount++
			return nil
		}).
		Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected batch enqueue error")
	}
	if catchCount != 1 {
		t.Fatalf("expected catch once, got %d", catchCount)
	}
	if finallyCount != 1 {
		t.Fatalf("expected finally once, got %d", finallyCount)
	}
	st, err := b.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find failed batch: %v", err)
	}
	if !st.Completed || !st.Cancelled {
		t.Fatalf("expected batch cancelled+completed, got %+v", st)
	}
	b.mu.RLock()
	cbCount := len(b.batchCallbacks)
	b.mu.RUnlock()
	if cbCount != 0 {
		t.Fatalf("expected batch callbacks cleaned, got %d", cbCount)
	}
}

func TestChainDispatchFailureStillReturnsChainID(t *testing.T) {
	q := newSyncTestRuntime()
	b, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:downsample", func(context.Context, Context) error { return errors.New("boom") })

	chainID, err := b.Chain(NewJob("monitor:downsample", nil)).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected chain dispatch error")
	}
	if chainID == "" {
		t.Fatal("expected non-empty chain id on dispatch error")
	}
}

func TestBatchDispatchFailureStillReturnsBatchID(t *testing.T) {
	q := newSyncTestRuntime()
	b, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:downsample", func(context.Context, Context) error { return errors.New("boom") })

	batchID, err := b.Batch(NewJob("monitor:downsample", nil)).Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected batch dispatch error")
	}
	if batchID == "" {
		t.Fatal("expected non-empty batch id on dispatch error")
	}
}
