package queue

import (
	"context"
	"testing"
)

// TestWithObserverReceivesEveryEventLayer verifies the root option spans the composed runtime.
func TestWithObserverReceivesEveryEventLayer(t *testing.T) {
	var events []Event
	observer := ObserverFunc(func(_ context.Context, event Event) {
		events = append(events, event)
	})

	q, err := NewSync(WithObserver(observer))
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	q.Register("reports:build", func(context.Context, Message) error { return nil })
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	if _, err := q.Dispatch(NewJob("reports:build").OnQueue("default")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	required := map[EventKind]EventLayer{
		EventDispatchStarted: EventLayerQueue,
		EventEnqueueAccepted: EventLayerQueue,
		EventProcessStarted:  EventLayerWorker,
		EventJobStarted:      EventLayerWorkflow,
	}
	for kind, layer := range required {
		event, ok := findEvent(events, kind)
		if !ok {
			t.Errorf("missing %q event in %+v", kind, events)
			continue
		}
		if event.Layer != layer {
			t.Errorf("event %q layer = %q, want %q", kind, event.Layer, layer)
		}
		if event.SchemaVersion == 0 || event.EventID == "" || event.Time.IsZero() {
			t.Errorf("event %q missing envelope metadata: %+v", kind, event)
		}
	}
}

// TestConfigObserverReceivesWorkflowEvents preserves the compatibility configuration path during migration.
func TestConfigObserverReceivesWorkflowEvents(t *testing.T) {
	var events []Event
	q, err := New(Config{
		Driver: DriverSync,
		Observer: ObserverFunc(func(_ context.Context, event Event) {
			events = append(events, event)
		}),
	})
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	q.Register("reports:build", func(context.Context, Message) error { return nil })
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	if _, err := q.Dispatch(NewJob("reports:build").OnQueue("default")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, ok := findEvent(events, EventJobSucceeded); !ok {
		t.Fatalf("config observer did not receive workflow events: %+v", events)
	}
}

// TestConfigAndOptionObserversShareOneEventIdentity prevents nested wrappers from describing one fact twice.
func TestConfigAndOptionObserversShareOneEventIdentity(t *testing.T) {
	var configEvents []Event
	var optionEvents []Event
	q, err := New(
		Config{
			Driver: DriverSync,
			Observer: ObserverFunc(func(_ context.Context, event Event) {
				configEvents = append(configEvents, event)
			}),
		},
		WithObserver(ObserverFunc(func(_ context.Context, event Event) {
			optionEvents = append(optionEvents, event)
		})),
	)
	if err != nil {
		t.Fatalf("new observed queue: %v", err)
	}
	q.Register("reports:identity", func(context.Context, Message) error { return nil })
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	if _, err := q.Dispatch(NewJob("reports:identity").OnQueue("default")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	for _, kind := range []EventKind{EventEnqueueAccepted, EventProcessStarted, EventJobSucceeded} {
		configMatches := eventsOfKind(configEvents, kind)
		optionMatches := eventsOfKind(optionEvents, kind)
		if len(configMatches) != 1 || len(optionMatches) != 1 {
			t.Fatalf("event %q counts = config:%d option:%d, want 1/1", kind, len(configMatches), len(optionMatches))
		}
		if configMatches[0].EventID != optionMatches[0].EventID || !configMatches[0].Time.Equal(optionMatches[0].Time) {
			t.Fatalf("event %q identity differs: config=%+v option=%+v", kind, configMatches[0], optionMatches[0])
		}
	}
}

// findEvent keeps assertions focused on the unified contract rather than incidental event ordering that will change when enqueue ordering is corrected.
func findEvent(events []Event, kind EventKind) (Event, bool) {
	for _, event := range events {
		if event.Kind == kind {
			return event, true
		}
	}
	return Event{}, false
}

// eventsOfKind returns every matching fact so duplicate emission is part of the observer contract assertion.
func eventsOfKind(events []Event, kind EventKind) []Event {
	matches := make([]Event, 0, 1)
	for _, event := range events {
		if event.Kind == kind {
			matches = append(matches, event)
		}
	}
	return matches
}
