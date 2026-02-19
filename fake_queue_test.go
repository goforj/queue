package queue

import (
	"context"
	"testing"
)

func TestFakeQueue_Assertions(t *testing.T) {
	fake := NewFake()
	fake.AssertNothingDispatched(t)

	_ = fake.Dispatch(NewTask("emails:send").Payload(map[string]int{"id": 1}).OnQueue("critical"))
	_ = fake.Dispatch(NewTask("emails:send").Payload(map[string]int{"id": 2}).OnQueue("critical"))
	_ = fake.Dispatch(NewTask("emails:cleanup").Payload(map[string]int{"id": 3}).OnQueue("default"))

	fake.AssertCount(t, 3)
	fake.AssertDispatched(t, "emails:send")
	fake.AssertDispatchedOn(t, "critical", "emails:send")
	fake.AssertDispatchedTimes(t, "emails:send", 2)
	fake.AssertNotDispatched(t, "unknown:task")
}

func TestFakeQueue_DispatchStructInfersTaskType(t *testing.T) {
	type EmailPayload struct {
		ID int `json:"id"`
	}

	fake := NewFake()
	if err := fake.DispatchCtx(context.Background(), EmailPayload{ID: 7}); err != nil {
		t.Fatalf("dispatch struct failed: %v", err)
	}

	records := fake.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Task.Type != "EmailPayload" {
		t.Fatalf("expected inferred type EmailPayload, got %q", records[0].Task.Type)
	}
	if records[0].Queue != "default" {
		t.Fatalf("expected default queue, got %q", records[0].Queue)
	}
}

func TestFakeQueue_ContextCanceled(t *testing.T) {
	fake := NewFake()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fake.DispatchCtx(ctx, NewTask("emails:send").Payload([]byte("{}")).OnQueue("default")); err == nil {
		t.Fatal("expected canceled context error")
	}
	fake.AssertNothingDispatched(t)
}
