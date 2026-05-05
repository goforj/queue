package queue

import "context"

import "testing"

type panicObserver struct{}

func (panicObserver) Observe(context.Context, Event) { panic("boom") }

func TestSafeObserve_RecoversObserverPanic(t *testing.T) {
	SafeObserve(context.Background(), panicObserver{}, Event{Kind: EventEnqueueAccepted})
}

func TestNormalizeQueueName_DefaultsEmpty(t *testing.T) {
	if got := normalizeQueueName(""); got != "default" {
		t.Fatalf("normalizeQueueName(\"\") = %q, want %q", got, "default")
	}
	if got := normalizeQueueName("critical"); got != "critical" {
		t.Fatalf("normalizeQueueName(\"critical\") = %q, want %q", got, "critical")
	}
}
