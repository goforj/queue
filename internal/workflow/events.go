package workflow

import (
	"context"
	"time"
)

// EventKind identifies one workflow lifecycle fact.
type EventKind string

const (
	// EventDispatchStarted marks the beginning of logical dispatch submission.
	EventDispatchStarted EventKind = "dispatch_started"
	// EventDispatchSucceeded records that a logical dispatch was accepted.
	EventDispatchSucceeded EventKind = "dispatch_succeeded"
	// EventDispatchFailed records that a logical dispatch was rejected.
	EventDispatchFailed EventKind = "dispatch_failed"
	// EventJobStarted records the beginning of a logical handler attempt.
	EventJobStarted EventKind = "job_started"
	// EventJobSucceeded records a committed logical job success.
	EventJobSucceeded EventKind = "job_succeeded"
	// EventJobFailed records a permanent or exhausted logical job failure.
	EventJobFailed EventKind = "job_failed"
	// EventChainStarted records creation and initial scheduling of a chain.
	EventChainStarted EventKind = "chain_started"
	// EventChainAdvanced records a committed transition to the next chain node.
	EventChainAdvanced EventKind = "chain_advanced"
	// EventChainCompleted records terminal chain success.
	EventChainCompleted EventKind = "chain_completed"
	// EventChainFailed records terminal chain failure.
	EventChainFailed EventKind = "chain_failed"
	// EventBatchStarted records creation and initial scheduling of a batch.
	EventBatchStarted EventKind = "batch_started"
	// EventBatchProgressed records a committed change to aggregate batch state.
	EventBatchProgressed EventKind = "batch_progressed"
	// EventBatchCompleted records terminal batch completion.
	EventBatchCompleted EventKind = "batch_completed"
	// EventBatchFailed records a logical batch failure.
	EventBatchFailed EventKind = "batch_failed"
	// EventBatchCancelled records cancellation after a batch can no longer continue.
	EventBatchCancelled EventKind = "batch_cancelled"
	// EventCallbackStarted records the beginning of an ephemeral callback attempt.
	EventCallbackStarted EventKind = "callback_started"
	// EventCallbackSucceeded records successful callback completion.
	EventCallbackSucceeded EventKind = "callback_succeeded"
	// EventCallbackFailed records callback failure.
	EventCallbackFailed EventKind = "callback_failed"
)

// Event carries internal workflow facts and correlation into public observer adapters.
type Event struct {
	SchemaVersion int
	EventID       string
	Kind          EventKind
	DispatchID    string
	JobID         string
	ChainID       string
	BatchID       string
	Attempt       int
	JobType       string
	JobKey        string
	Queue         string
	Duration      time.Duration
	Time          time.Time
	Err           error
}

// Observer receives internal workflow events.
type Observer interface {
	// Observe consumes one best-effort workflow fact.
	Observe(ctx context.Context, event Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(ctx context.Context, event Event)

// Observe calls the wrapped observer function.
func (f ObserverFunc) Observe(ctx context.Context, event Event) {
	f(ctx, event)
}

// MultiObserver fans out one event to multiple observers.
func MultiObserver(observers ...Observer) Observer {
	filtered := make([]Observer, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			filtered = append(filtered, observer)
		}
	}
	return multiObserver(filtered)
}

type multiObserver []Observer

// Observe forwards one event to every configured observer while preserving panic isolation.
func (m multiObserver) Observe(ctx context.Context, event Event) {
	for _, observer := range m {
		safeObserve(ctx, observer, event)
	}
}

// safeObserve prevents observer panics or nil contexts from changing workflow execution.
func safeObserve(ctx context.Context, observer Observer, event Event) {
	if observer == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		_ = recover()
	}()
	observer.Observe(ctx, event)
}
