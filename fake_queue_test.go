package queue

import (
	"context"
	"testing"
)

func TestFakeQueue_Assertions(t *testing.T) {
	fake := NewFake()
	fake.AssertNothingDispatched(t)

	_ = fake.Dispatch(NewJob("emails:send").Payload(map[string]int{"id": 1}).OnQueue("critical"))
	_ = fake.Dispatch(NewJob("emails:send").Payload(map[string]int{"id": 2}).OnQueue("critical"))
	_ = fake.Dispatch(NewJob("emails:cleanup").Payload(map[string]int{"id": 3}).OnQueue("default"))

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
	if records[0].Job.Type != "EmailPayload" {
		t.Fatalf("expected inferred type EmailPayload, got %q", records[0].Job.Type)
	}
	if records[0].Queue != "default" {
		t.Fatalf("expected default queue, got %q", records[0].Queue)
	}
}

func TestFakeQueue_ContextCanceled(t *testing.T) {
	fake := NewFake()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fake.DispatchCtx(ctx, NewJob("emails:send").Payload([]byte("{}")).OnQueue("default")); err == nil {
		t.Fatal("expected canceled context error")
	}
	fake.AssertNothingDispatched(t)
}

func TestFakeQueue_NoopRuntimeMethodsAndReset(t *testing.T) {
	fake := NewFake()
	if fake.Driver() != DriverNull {
		t.Fatalf("expected fake driver %q, got %q", DriverNull, fake.Driver())
	}
	fake.Register("job:noop", func(context.Context, Job) error { return nil })
	if err := fake.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers noop failed: %v", err)
	}
	if got := fake.Workers(5); got != fake {
		t.Fatal("expected Workers to return same fake queue")
	}
	if err := fake.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown noop failed: %v", err)
	}

	_ = fake.Dispatch(NewJob("job:one").OnQueue("default"))
	if len(fake.Records()) != 1 {
		t.Fatalf("expected one record before reset, got %d", len(fake.Records()))
	}
	fake.Reset()
	if len(fake.Records()) != 0 {
		t.Fatalf("expected no records after reset, got %d", len(fake.Records()))
	}
}
