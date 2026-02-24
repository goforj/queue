package queue

import "testing"

type panicObserver struct{}

func (panicObserver) Observe(Event) { panic("boom") }

func TestSafeObserve_RecoversObserverPanic(t *testing.T) {
	SafeObserve(panicObserver{}, Event{Kind: EventEnqueueAccepted})
}

func TestNormalizeQueueName_DefaultsEmpty(t *testing.T) {
	if got := NormalizeQueueName(""); got != "default" {
		t.Fatalf("NormalizeQueueName(\"\") = %q, want %q", got, "default")
	}
	if got := NormalizeQueueName("critical"); got != "critical" {
		t.Fatalf("NormalizeQueueName(\"critical\") = %q, want %q", got, "critical")
	}
}
