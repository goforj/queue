package queue

import (
	"context"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

type fakePayloadWithJobType struct {
	Value string `json:"value"`
}

func (p fakePayloadWithJobType) JobType() string { return "custom:type" }

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
	fake.AssertNotDispatched(t, "unknown:job")
}

func TestFakeQueue_DispatchStructInfersJobType(t *testing.T) {
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

func TestFakeQueue_BusAdapterMethods(t *testing.T) {
	fake := NewFake()

	// No-op registration should not panic and is part of the busruntime adapter surface.
	fake.BusRegister("job:noop", func(context.Context, busruntime.InboundJob) error { return nil })

	opts := busruntime.JobOptions{
		Queue:     "critical",
		Delay:     50 * time.Millisecond,
		Timeout:   200 * time.Millisecond,
		Retry:     3,
		Backoff:   25 * time.Millisecond,
		UniqueFor: 1 * time.Second,
	}
	if err := fake.BusDispatch(context.Background(), "emails:send", []byte(`{"id":1}`), opts); err != nil {
		t.Fatalf("bus dispatch failed: %v", err)
	}

	records := fake.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.Queue != "critical" {
		t.Fatalf("expected queue critical, got %q", rec.Queue)
	}
	if rec.Job.Type != "emails:send" {
		t.Fatalf("expected job type emails:send, got %q", rec.Job.Type)
	}
	jobOpts := rec.Job.jobOptions()
	if jobOpts.queueName != "critical" || jobOpts.delay != opts.Delay || jobOpts.uniqueTTL != opts.UniqueFor {
		t.Fatalf("unexpected job opts: %+v", jobOpts)
	}
	if jobOpts.timeout == nil || *jobOpts.timeout != opts.Timeout {
		t.Fatalf("expected timeout %v, got %+v", opts.Timeout, jobOpts.timeout)
	}
	if jobOpts.maxRetry == nil || *jobOpts.maxRetry != opts.Retry {
		t.Fatalf("expected retry %d, got %+v", opts.Retry, jobOpts.maxRetry)
	}
	if jobOpts.backoff == nil || *jobOpts.backoff != opts.Backoff {
		t.Fatalf("expected backoff %v, got %+v", opts.Backoff, jobOpts.backoff)
	}
}

func TestFakeQueue_DispatchTypeInferenceEdges(t *testing.T) {
	type namedAlias struct {
		ID int `json:"id"`
	}
	type namedPtr struct {
		Name string `json:"name"`
	}

	fake := NewFake()
	if err := fake.Dispatch(fakePayloadWithJobType{Value: "x"}); err != nil {
		t.Fatalf("dispatch fakePayloadWithJobType failed: %v", err)
	}
	if got := fake.Records()[0].Job.Type; got != "custom:type" {
		t.Fatalf("expected custom JobType override, got %q", got)
	}

	if err := fake.Dispatch(&namedPtr{Name: "ptr"}); err != nil {
		t.Fatalf("dispatch pointer struct failed: %v", err)
	}
	if got := fake.Records()[1].Job.Type; got != "namedPtr" {
		t.Fatalf("expected inferred pointer element type namedPtr, got %q", got)
	}

	if err := fake.Dispatch([]string{"x"}); err == nil {
		t.Fatal("expected anonymous/slice payload inference error")
	}
	if err := fake.Dispatch(func() {}); err == nil {
		t.Fatal("expected function payload inference or marshal error")
	}

	// Ensure named non-Job struct still infers type and records.
	if err := fake.Dispatch(namedAlias{ID: 3}); err != nil {
		t.Fatalf("dispatch namedAlias failed: %v", err)
	}
	if got := fake.Records()[2].Job.Type; got != "namedAlias" {
		t.Fatalf("expected namedAlias type, got %q", got)
	}
}
