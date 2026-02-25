package queue

import "testing"

type panicObserver struct{}

func (panicObserver) Observe(Event) { panic("boom") }

func TestSafeObserve_RecoversObserverPanic(t *testing.T) {
	SafeObserve(panicObserver{}, Event{Kind: EventEnqueueAccepted})
}

func TestNormalizeQueueName_DefaultsEmpty(t *testing.T) {
	if got := normalizeQueueName(""); got != "default" {
		t.Fatalf("normalizeQueueName(\"\") = %q, want %q", got, "default")
	}
	if got := normalizeQueueName("critical"); got != "critical" {
		t.Fatalf("normalizeQueueName(\"critical\") = %q, want %q", got, "critical")
	}
}
