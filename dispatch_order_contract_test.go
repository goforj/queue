package queue_test

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue"
)

// TestSyncDispatchObservationOrder verifies acceptance precedes inline execution on success.
func TestSyncDispatchObservationOrder(t *testing.T) {
	recorder := &retryEventRecorder{}
	q, err := queue.NewSync(queue.WithObserver(recorder))
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	q.Register("contract:order:success", func(context.Context, queue.Message) error { return nil })
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	if _, err := q.Dispatch(queue.NewJob("contract:order:success")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	assertEventKinds(t, recorder.snapshot(), []queue.EventKind{
		queue.EventDispatchStarted,
		queue.EventEnqueueAccepted,
		queue.EventProcessStarted,
		queue.EventJobStarted,
		queue.EventJobSucceeded,
		queue.EventProcessSucceeded,
		queue.EventDispatchSucceeded,
	})
}

// TestSyncExecutionFailureRemainsAccepted verifies business failure is not reported as enqueue rejection.
func TestSyncExecutionFailureRemainsAccepted(t *testing.T) {
	recorder := &retryEventRecorder{}
	q, err := queue.NewSync(queue.WithObserver(recorder))
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	wantErr := errors.New("business failure")
	q.Register("contract:order:failure", func(context.Context, queue.Message) error { return wantErr })
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	if _, err := q.Dispatch(queue.NewJob("contract:order:failure")); !errors.Is(err, wantErr) {
		t.Fatalf("dispatch error = %v, want business failure", err)
	}
	assertEventKinds(t, recorder.snapshot(), []queue.EventKind{
		queue.EventDispatchStarted,
		queue.EventEnqueueAccepted,
		queue.EventProcessStarted,
		queue.EventJobStarted,
		queue.EventJobFailed,
		queue.EventProcessFailed,
		queue.EventDispatchSucceeded,
	})
}

// assertEventKinds compares exact synchronous causality without coupling assertions to timestamps or IDs.
func assertEventKinds(t *testing.T, events []queue.Event, want []queue.EventKind) {
	t.Helper()
	got := make([]queue.EventKind, len(events))
	for index, event := range events {
		got[index] = event.Kind
	}
	if len(got) != len(want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("event kinds = %v, want %v", got, want)
		}
	}
}
