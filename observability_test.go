package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

func startTestQueue(t *testing.T, q queueRuntime) {
	t.Helper()
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
}

func TestStatsCollector_CapturesQueueProcessing(t *testing.T) {
	collector := NewStatsCollector()
	q, err := newRuntime(Config{
		Driver:   DriverSync,
		Observer: collector,
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	startTestQueue(t, q)
	q.Register("job:obs:ok", func(_ context.Context, _ Job) error { return nil })
	if err := q.Dispatch(NewJob("job:obs:ok").Payload([]byte(`{}`)).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	stats := collector.Snapshot()
	counters, ok := stats.ByQueue["default"]
	if !ok {
		t.Fatal("expected default queue counters")
	}
	if counters.Processed < 1 {
		t.Fatalf("expected processed >= 1, got %d", counters.Processed)
	}
	if counters.Active != 0 {
		t.Fatalf("expected active = 0, got %d", counters.Active)
	}
}

func TestStatsCollector_CapturesProcessingFailure(t *testing.T) {
	collector := NewStatsCollector()
	q, err := newRuntime(Config{
		Driver:   DriverSync,
		Observer: collector,
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	startTestQueue(t, q)
	q.Register("job:obs:fail", func(_ context.Context, _ Job) error { return errors.New("boom") })
	_ = q.Dispatch(NewJob("job:obs:fail").Payload([]byte(`{}`)).OnQueue("default").Retry(0))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := collector.Snapshot()
		counters := stats.ByQueue["default"]
		if counters.Failed >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected failed counter to be incremented")
}

// TestStatsCollector_WorkerpoolPanicClosesActive verifies panic recovery does
// not leave the observer gauge or its per-delivery correlation state live.
func TestStatsCollector_WorkerpoolPanicClosesActive(t *testing.T) {
	collector := NewStatsCollector()
	q, err := New(
		Config{Driver: DriverWorkerpool},
		WithObserver(collector),
		WithWorkers(1),
	)
	if err != nil {
		t.Fatalf("new workerpool queue: %v", err)
	}
	q.Register("job:observer:panic", func(context.Context, Message) error {
		panic("handler panic")
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workerpool: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown workerpool: %v", shutdownErr)
		}
	})
	if _, err := q.Dispatch(NewJob("job:observer:panic")); err != nil {
		t.Fatalf("dispatch panicking job: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		counters, ok := collector.Snapshot().Queue("default")
		if ok && counters.Failed == 1 && counters.Active == 0 {
			collector.mu.RLock()
			state := collector.byQueue["default"]
			activeKeys := len(state.activeByKey)
			activeSettlements := len(state.activeSettlements)
			uncorrelatedActive := state.uncorrelatedActive
			collector.mu.RUnlock()
			if activeKeys != 0 || activeSettlements != 0 || uncorrelatedActive != 0 {
				t.Fatalf("panic correlation state = keys:%d settlements:%d uncorrelated:%d, want empty", activeKeys, activeSettlements, uncorrelatedActive)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("panic counters = %+v, want failed=1 active=0", collector.Snapshot().ByQueue["default"])
}

// TestHandlerPanicErrorPreservesErrorIdentity verifies panic telemetry remains
// useful to errors.Is callers without changing non-error panic formatting.
func TestHandlerPanicErrorPreservesErrorIdentity(t *testing.T) {
	sentinel := errors.New("panic sentinel")
	if err := handlerPanicError(sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("error panic = %v, want wrapped sentinel", err)
	}
	if err := handlerPanicError("panic value"); err == nil || err.Error() != "handler panicked: panic value" {
		t.Fatalf("value panic = %v, want stable diagnostic", err)
	}
}

// TestWrapObservedHandlerReportsAndRepanics pins failure telemetry without
// turning a synchronous backend panic into an ordinary returned error.
func TestWrapObservedHandlerReportsAndRepanics(t *testing.T) {
	var events []Event
	wrapped := wrapObservedHandler(
		ObserverFunc(func(_ context.Context, event Event) { events = append(events, event) }),
		DriverSync,
		"default",
		"job:panic",
		nil,
		func(context.Context, Job) error { panic("panic value") },
	)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = wrapped(context.Background(), NewJob("job:panic"))
	}()
	if recovered != "panic value" {
		t.Fatalf("recovered panic = %#v, want original value", recovered)
	}
	if len(events) != 2 || events[0].Kind != EventProcessStarted || events[1].Kind != EventProcessFailed {
		t.Fatalf("panic events = %+v, want process_started then process_failed", events)
	}
	if events[1].Err == nil || events[1].Err.Error() != "handler panicked: panic value" {
		t.Fatalf("panic failure error = %v, want stable diagnostic", events[1].Err)
	}
}

// TestStatsCollector_SettlementFailureClosesActive verifies unresolved broker settlement cannot leak active delivery gauges or fabricate an application outcome.
func TestStatsCollector_SettlementFailureClosesActive(t *testing.T) {
	collector := NewStatsCollector()
	now := time.Now()
	ctx, _ := busruntime.WithDeliverySettlement(context.Background())
	collector.Observe(ctx, Event{
		Kind:   EventProcessStarted,
		Driver: DriverSQS,
		Queue:  "default",
		JobID:  "job-settlement",
		JobKey: "job-settlement",
		Time:   now,
	})
	collector.Observe(ctx, Event{
		Kind:   EventProcessStarted,
		Driver: DriverSQS,
		Queue:  "default",
		JobID:  "job-settlement",
		JobKey: "job-settlement",
		Time:   now,
	})
	if active := collector.Snapshot().Active("default"); active != 1 {
		t.Fatalf("duplicate start active = %d, want 1 for the same physical identity", active)
	}
	collector.Observe(ctx, Event{
		Kind:   EventSettlementFailed,
		Driver: DriverSQS,
		Queue:  "default",
		JobID:  "job-settlement",
		JobKey: "job-settlement",
		Err:    errors.New("delete failed"),
		Time:   now.Add(time.Millisecond),
	})
	counters, ok := collector.Snapshot().Queue("default")
	if !ok {
		t.Fatal("expected settlement queue counters")
	}
	if counters.Active != 0 || counters.Processed != 0 || counters.Failed != 0 {
		t.Fatalf("settlement counters = %+v, want active closed without terminal application count", counters)
	}
}

// TestStatsCollector_SettlementFailureDoesNotGuessUncorrelatedActive verifies
// settlement facts without physical identity cannot consume a live gauge.
func TestStatsCollector_SettlementFailureDoesNotGuessUncorrelatedActive(t *testing.T) {
	collector := NewStatsCollector()
	now := time.Now()
	collector.Observe(context.Background(), Event{
		Kind:  EventProcessStarted,
		Queue: "default",
		Time:  now,
	})
	collector.Observe(context.Background(), Event{
		Kind:  EventSettlementFailed,
		Queue: "default",
		Err:   errors.New("settlement failed"),
		Time:  now.Add(time.Millisecond),
	})

	counters, ok := collector.Snapshot().Queue("default")
	if !ok {
		t.Fatal("expected default queue counters")
	}
	if counters.Active != 1 || counters.Processed != 0 || counters.Failed != 0 {
		t.Fatalf("context-free settlement counters = %+v, want active unchanged without physical identity", counters)
	}
}

// TestStatsCollector_LateIdentitylessSettlementCannotCloseNewExecution pins
// sequential tuple reuse, where event fields cannot identify the old receipt.
func TestStatsCollector_LateIdentitylessSettlementCannotCloseNewExecution(t *testing.T) {
	collector := NewStatsCollector()
	now := time.Now()
	oldCtx, _ := busruntime.WithDeliverySettlement(context.Background())
	newCtx, _ := busruntime.WithDeliverySettlement(context.Background())
	base := Event{
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-reused",
		JobID:      "job-reused",
		Attempt:    1,
	}

	started := base
	started.Kind = EventProcessStarted
	started.Time = now
	collector.Observe(oldCtx, started)
	failed := base
	failed.Kind = EventProcessFailed
	failed.Err = errors.New("old handler failed")
	failed.Time = now.Add(time.Millisecond)
	collector.Observe(oldCtx, failed)
	started.Time = now.Add(2 * time.Millisecond)
	collector.Observe(newCtx, started)

	lateSettlement := base
	lateSettlement.Kind = EventSettlementFailed
	lateSettlement.Err = errors.New("old acknowledgement failed")
	lateSettlement.Time = now.Add(3 * time.Millisecond)
	collector.Observe(context.Background(), lateSettlement)
	if active := collector.Snapshot().Active("default"); active != 1 {
		t.Fatalf("active after late identity-less settlement = %d, want newer execution retained", active)
	}

	succeeded := base
	succeeded.Kind = EventProcessSucceeded
	succeeded.Time = now.Add(4 * time.Millisecond)
	collector.Observe(newCtx, succeeded)
	counters, _ := collector.Snapshot().Queue("default")
	if counters.Active != 0 || counters.Failed != 1 || counters.Processed != 1 {
		t.Fatalf("reused tuple terminal counters = %+v, want both physical executions closed once", counters)
	}
}

// TestStatsCollector_SettlementFailureClosesOnlyItsExecution verifies a failed
// acknowledgement cannot consume another delivery's active gauge after the
// same handler attempt already emitted process_failed.
func TestStatsCollector_SettlementFailureClosesOnlyItsExecution(t *testing.T) {
	collector := NewStatsCollector()
	now := time.Now()
	failedCtx, _ := busruntime.WithDeliverySettlement(context.Background())
	runningCtx, _ := busruntime.WithDeliverySettlement(context.Background())

	collector.Observe(failedCtx, Event{
		Kind:       EventProcessStarted,
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-failed",
		JobID:      "job-failed",
		Time:       now,
	})
	collector.Observe(runningCtx, Event{
		Kind:       EventProcessStarted,
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-running",
		JobID:      "job-running",
		Time:       now.Add(time.Millisecond),
	})
	collector.Observe(failedCtx, Event{
		Kind:       EventProcessFailed,
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-failed",
		JobID:      "job-failed",
		Err:        errors.New("handler failed"),
		Time:       now.Add(2 * time.Millisecond),
	})
	collector.Observe(failedCtx, Event{
		Kind:       EventSettlementFailed,
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-failed",
		JobID:      "job-failed",
		Err:        errors.New("delete failed"),
		Time:       now.Add(3 * time.Millisecond),
	})

	counters, ok := collector.Snapshot().Queue("default")
	if !ok {
		t.Fatal("expected default queue counters")
	}
	if counters.Active != 1 || counters.Failed != 1 || counters.Processed != 0 {
		t.Fatalf("counters after failed settlement = %+v, want one unrelated active execution and one handler failure", counters)
	}

	collector.Observe(runningCtx, Event{
		Kind:       EventProcessSucceeded,
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-running",
		JobID:      "job-running",
		Time:       now.Add(4 * time.Millisecond),
	})
	counters, _ = collector.Snapshot().Queue("default")
	if counters.Active != 0 || counters.Failed != 1 || counters.Processed != 1 {
		t.Fatalf("terminal counters = %+v, want both executions closed exactly once", counters)
	}
}

// TestStatsCollector_SettlementFailureRequiresIdentityForDuplicateTuple
// verifies event correlation cannot masquerade as physical delivery identity.
func TestStatsCollector_SettlementFailureRequiresIdentityForDuplicateTuple(t *testing.T) {
	collector := NewStatsCollector()
	now := time.Now()
	base := Event{
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-duplicate",
		JobID:      "job-duplicate",
		Attempt:    2,
	}
	started := base
	started.Kind = EventProcessStarted
	started.Time = now
	collector.Observe(context.Background(), started)
	started.Time = now.Add(time.Millisecond)
	collector.Observe(context.Background(), started)
	ambiguousSettlement := base
	ambiguousSettlement.Kind = EventSettlementFailed
	ambiguousSettlement.Err = errors.New("ambiguous ack failed")
	ambiguousSettlement.Time = now.Add(1500 * time.Microsecond)
	collector.Observe(context.Background(), ambiguousSettlement)
	if active := collector.Snapshot().Active("default"); active != 2 {
		t.Fatalf("ambiguous settlement active = %d, want both indistinguishable executions retained", active)
	}

	failed := base
	failed.Kind = EventProcessFailed
	failed.Err = errors.New("handler failed")
	failed.Time = now.Add(2 * time.Millisecond)
	collector.Observe(context.Background(), failed)
	settlementFailed := base
	settlementFailed.Kind = EventSettlementFailed
	settlementFailed.Err = errors.New("ack failed")
	settlementFailed.Time = now.Add(3 * time.Millisecond)
	collector.Observe(context.Background(), settlementFailed)

	counters, ok := collector.Snapshot().Queue("default")
	if !ok {
		t.Fatal("expected default queue counters")
	}
	if counters.Active != 1 || counters.Failed != 1 {
		t.Fatalf("duplicate tuple counters after settlement failure = %+v, want one unrelated physical duplicate active", counters)
	}

	succeeded := base
	succeeded.Kind = EventProcessSucceeded
	succeeded.Time = now.Add(4 * time.Millisecond)
	collector.Observe(context.Background(), succeeded)
	counters, _ = collector.Snapshot().Queue("default")
	if counters.Active != 0 || counters.Failed != 1 || counters.Processed != 1 {
		t.Fatalf("duplicate tuple terminal counters = %+v, want both physical deliveries closed once", counters)
	}
}

// TestStatsCollector_SettlementFailureRequiresPhysicalIdentity verifies the
// collector fails closed when an older or custom driver loses settlement context.
func TestStatsCollector_SettlementFailureRequiresPhysicalIdentity(t *testing.T) {
	for _, test := range []struct {
		name               string
		startWithIdentity  bool
		settleWithIdentity bool
		wantActive         int64
	}{
		{name: "context-free", wantActive: 1},
		{name: "mixed-version lost identity", startWithIdentity: true, wantActive: 1},
		{name: "current identity", startWithIdentity: true, settleWithIdentity: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := NewStatsCollector()
			now := time.Now()
			startCtx := context.Background()
			if test.startWithIdentity {
				startCtx, _ = busruntime.WithDeliverySettlement(startCtx)
			}
			settlementCtx := context.Background()
			if test.settleWithIdentity {
				settlementCtx = startCtx
			}
			started := Event{
				Kind:       EventProcessStarted,
				Driver:     DriverSQS,
				Queue:      "default",
				DispatchID: "dispatch-legacy",
				JobID:      "job-legacy",
				Attempt:    1,
				Time:       now,
			}
			collector.Observe(startCtx, started)
			settlementFailed := started
			settlementFailed.Kind = EventSettlementFailed
			settlementFailed.Err = errors.New("legacy delete failed")
			settlementFailed.Time = now.Add(time.Millisecond)
			collector.Observe(settlementCtx, settlementFailed)

			counters, ok := collector.Snapshot().Queue("default")
			if !ok {
				t.Fatal("expected default queue counters")
			}
			if counters.Active != test.wantActive || counters.Processed != 0 || counters.Failed != 0 {
				t.Fatalf("settlement counters = %+v, want active=%d and no terminal application count", counters, test.wantActive)
			}
		})
	}

	collector := NewStatsCollector()
	ctx, _ := busruntime.WithDeliverySettlement(context.Background())
	started := Event{
		Kind:       EventProcessStarted,
		Driver:     DriverSQS,
		Queue:      "default",
		DispatchID: "dispatch-lost-process-context",
		JobID:      "job-lost-process-context",
		Time:       time.Now(),
	}
	collector.Observe(ctx, started)
	succeeded := started
	succeeded.Kind = EventProcessSucceeded
	succeeded.Time = started.Time.Add(time.Millisecond)
	collector.Observe(context.Background(), succeeded)
	if counters, _ := collector.Snapshot().Queue("default"); counters.Active != 1 || counters.Processed != 1 {
		t.Fatalf("identity-less process terminal counters = %+v, want exact execution retained", counters)
	}
	settlementFailed := started
	settlementFailed.Kind = EventSettlementFailed
	settlementFailed.Err = errors.New("late exact settlement")
	settlementFailed.Time = started.Time.Add(2 * time.Millisecond)
	collector.Observe(ctx, settlementFailed)
	if active := collector.Snapshot().Active("default"); active != 0 {
		t.Fatalf("late exact settlement active = %d, want already-closed execution unchanged", active)
	}
}

// TestCollectorQueueStateRejectsMissingActiveClose verifies a terminal fact
// cannot decrement a queue that has no matching execution state.
func TestCollectorQueueStateRejectsMissingActiveClose(t *testing.T) {
	state := collectorQueueState{}
	if state.closeByKey("missing") {
		t.Fatal("missing execution unexpectedly closed")
	}
}

func TestStatsSnapshot_Getters(t *testing.T) {
	collector := NewStatsCollector()
	now := time.Now()
	collector.Observe(context.Background(), Event{
		Kind:   EventEnqueueAccepted,
		Driver: DriverSync,
		Queue:  "default",
		Time:   now,
	})
	collector.Observe(context.Background(), Event{
		Kind:     EventProcessStarted,
		Driver:   DriverSync,
		Queue:    "default",
		JobKey:   "job-1",
		Time:     now.Add(10 * time.Millisecond),
		Duration: 5 * time.Millisecond,
	})
	collector.Observe(context.Background(), Event{
		Kind:     EventProcessSucceeded,
		Driver:   DriverSync,
		Queue:    "default",
		JobKey:   "job-1",
		Time:     now.Add(20 * time.Millisecond),
		Duration: 5 * time.Millisecond,
	})

	snapshot := collector.Snapshot()
	counters, ok := snapshot.Queue("default")
	if !ok {
		t.Fatal("expected default queue from getter")
	}
	if counters.Processed < 1 {
		t.Fatalf("expected processed >= 1, got %d", counters.Processed)
	}
	throughput, ok := snapshot.Throughput("default")
	if !ok {
		t.Fatal("expected default throughput from getter")
	}
	if throughput.Hour.Processed < 1 {
		t.Fatalf("expected hour processed >= 1, got %d", throughput.Hour.Processed)
	}
	names := snapshot.Queues()
	if len(names) != 1 || names[0] != "default" {
		t.Fatalf("expected queue names [default], got %v", names)
	}
}

func TestObserverPanic_DoesNotBreakDispatch(t *testing.T) {
	q, err := newRuntime(Config{
		Driver: DriverSync,
		Observer: ObserverFunc(func(context.Context, Event) {
			panic("observer panic")
		}),
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	startTestQueue(t, q)
	q.Register("job:panic:enqueue", func(_ context.Context, _ Job) error { return nil })
	if err := q.Dispatch(NewJob("job:panic:enqueue").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
}

func TestObserverPanic_DoesNotBreakHandlerExecution(t *testing.T) {
	var called atomic.Int64
	q, err := newRuntime(Config{
		Driver: DriverSync,
		Observer: ObserverFunc(func(context.Context, Event) {
			panic("observer panic")
		}),
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	startTestQueue(t, q)
	q.Register("job:panic:handler", func(_ context.Context, _ Job) error {
		called.Add(1)
		return nil
	})
	if err := q.Dispatch(NewJob("job:panic:handler").OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if called.Load() != 1 {
		t.Fatalf("expected handler to run once, got %d", called.Load())
	}
}

func TestMultiObserverPanic_DoesNotBlockOtherObservers(t *testing.T) {
	var received atomic.Int64
	observer := MultiObserver(
		ObserverFunc(func(context.Context, Event) {
			panic("observer panic")
		}),
		ObserverFunc(func(context.Context, Event) {
			received.Add(1)
		}),
	)
	observer.Observe(context.Background(), Event{Kind: EventEnqueueAccepted, Queue: "default"})
	if received.Load() != 1 {
		t.Fatalf("expected second observer to receive event, got %d", received.Load())
	}
}
