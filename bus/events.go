package bus

import (
	"context"
	"time"
)

// EventKind identifies one legacy workflow lifecycle fact.
//
// Deprecated: use queue.EventKind.
type EventKind string

const (
	// EventDispatchStarted identifies the start of a legacy dispatch operation.
	EventDispatchStarted EventKind = "dispatch_started"
	// EventDispatchSucceeded identifies an accepted legacy dispatch operation.
	EventDispatchSucceeded EventKind = "dispatch_succeeded"
	// EventDispatchFailed identifies a rejected legacy dispatch operation.
	EventDispatchFailed EventKind = "dispatch_failed"
	// EventJobStarted identifies the start of logical workflow job execution.
	EventJobStarted EventKind = "job_started"
	// EventJobSucceeded identifies committed logical workflow job success.
	EventJobSucceeded EventKind = "job_succeeded"
	// EventJobFailed identifies terminal logical workflow job failure.
	EventJobFailed EventKind = "job_failed"
	// EventChainStarted identifies creation of a chain workflow.
	EventChainStarted EventKind = "chain_started"
	// EventChainAdvanced identifies committed advancement of a chain workflow.
	EventChainAdvanced EventKind = "chain_advanced"
	// EventChainCompleted identifies successful completion of a chain workflow.
	EventChainCompleted EventKind = "chain_completed"
	// EventChainFailed identifies terminal failure of a chain workflow.
	EventChainFailed EventKind = "chain_failed"
	// EventBatchStarted identifies creation of a batch workflow.
	EventBatchStarted EventKind = "batch_started"
	// EventBatchProgressed identifies committed progress of a batch workflow.
	EventBatchProgressed EventKind = "batch_progressed"
	// EventBatchCompleted identifies completion of a batch workflow.
	EventBatchCompleted EventKind = "batch_completed"
	// EventBatchFailed identifies a failed member of a batch workflow.
	EventBatchFailed EventKind = "batch_failed"
	// EventBatchCancelled identifies cancellation of a batch workflow.
	EventBatchCancelled EventKind = "batch_cancelled"
	// EventCallbackStarted identifies the start of an ephemeral callback.
	EventCallbackStarted EventKind = "callback_started"
	// EventCallbackSucceeded identifies successful completion of an ephemeral callback.
	EventCallbackSucceeded EventKind = "callback_succeeded"
	// EventCallbackFailed identifies failure of an ephemeral callback.
	EventCallbackFailed EventKind = "callback_failed"
)

// Event carries the legacy bus workflow event shape.
//
// Deprecated: use queue.Event. This shape remains available only at the bus
// compatibility boundary and is translated from the canonical producer.
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

// Observer receives legacy bus workflow events.
//
// Deprecated: use queue.Observer.
type Observer interface {
	Observe(ctx context.Context, event Event)
}

// ObserverFunc adapts a function to Observer.
//
// Deprecated: use queue.ObserverFunc.
type ObserverFunc func(ctx context.Context, event Event)

// Observe calls the wrapped observer function.
func (f ObserverFunc) Observe(ctx context.Context, event Event) {
	f(ctx, event)
}

// MultiObserver fans out one legacy event while isolating observer panics.
//
// Deprecated: use queue.MultiObserver.
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

// Observe forwards the unchanged legacy event to each configured observer.
func (m multiObserver) Observe(ctx context.Context, event Event) {
	for _, observer := range m {
		safeObserve(ctx, observer, event)
	}
}

// safeObserve prevents optional telemetry from changing workflow execution.
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
