package bus

import (
	"context"
	"time"
)

type EventKind string

const (
	EventDispatchStarted   EventKind = "dispatch_started"
	EventDispatchSucceeded EventKind = "dispatch_succeeded"
	EventDispatchFailed    EventKind = "dispatch_failed"
	EventJobStarted        EventKind = "job_started"
	EventJobSucceeded      EventKind = "job_succeeded"
	EventJobFailed         EventKind = "job_failed"
	EventChainStarted      EventKind = "chain_started"
	EventChainAdvanced     EventKind = "chain_advanced"
	EventChainCompleted    EventKind = "chain_completed"
	EventChainFailed       EventKind = "chain_failed"
	EventBatchStarted      EventKind = "batch_started"
	EventBatchProgressed   EventKind = "batch_progressed"
	EventBatchCompleted    EventKind = "batch_completed"
	EventBatchFailed       EventKind = "batch_failed"
	EventBatchCancelled    EventKind = "batch_cancelled"
	EventCallbackStarted   EventKind = "callback_started"
	EventCallbackSucceeded EventKind = "callback_succeeded"
	EventCallbackFailed    EventKind = "callback_failed"
)

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
	Queue         string
	Duration      time.Duration
	Time          time.Time
	Err           error
}

type Observer interface {
	Observe(ctx context.Context, event Event)
}

type ObserverFunc func(ctx context.Context, event Event)

// Observe calls the wrapped observer function.
// @group Events
//
// Example: observer func
//
//	observer := bus.ObserverFunc(func(ctx context.Context, event bus.Event) {
//		_ = event.Kind
//	})
//	observer.Observe(context.Background(), bus.Event{Kind: bus.EventDispatchStarted})
func (f ObserverFunc) Observe(ctx context.Context, event Event) {
	f(ctx, event)
}

// MultiObserver fans out one event to multiple observers.
// @group Events
//
// Example: fan out observers
//
//	observer := bus.MultiObserver(
//		bus.ObserverFunc(func(context.Context, bus.Event) {}),
//		bus.ObserverFunc(func(context.Context, bus.Event) {}),
//	)
//	observer.Observe(context.Background(), bus.Event{Kind: bus.EventDispatchStarted})
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

func (m multiObserver) Observe(ctx context.Context, event Event) {
	for _, observer := range m {
		safeObserve(ctx, observer, event)
	}
}

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
