package queue

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// awaitContinuations waits for committed hook outcomes under a fixed test budget.
func awaitContinuations(t *testing.T, q *Queue, id string, count int) []ContinuationOutcome {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		outcomes, err := q.FindContinuations(t.Context(), id)
		if err == nil && len(outcomes) == count {
			done := true
			for _, outcome := range outcomes {
				done = done && (outcome.Status == "succeeded" || outcome.Status == "exhausted")
			}
			if done {
				return outcomes
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("continuations did not settle: %+v %v", outcomes, err)
		case <-tick.C:
		}
	}
}

// TestMemoryNamedContinuationsPreservePayloadAndOrder verifies additive binding and immutable metadata.
func TestMemoryNamedContinuationsPreservePayloadAndOrder(t *testing.T) {
	q, err := NewWorkerpool(WithWorkers(3), WithResourceID("continuation-test"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	q.Register("business", func(context.Context, Message) error { return nil })
	seen := make(chan Continuation, 8)
	q.Register("hook", func(_ context.Context, m Message) error {
		value, ok := m.Continuation()
		if !ok || string(m.PayloadBytes()) != "unchanged" {
			return Permanent(fmt.Errorf("invalid invocation"))
		}
		seen <- value
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := Continue(q.Batch(NewJob("business"), NewJob("business")).Name("test")).OnProgress(NewJob("hook").Payload([]byte("unchanged"))).OnSuccess(NewJob("hook").Payload([]byte("unchanged"))).OnCompletion(NewJob("hook").Payload([]byte("unchanged"))).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	outcomes := awaitContinuations(t, q, id, 4)
	for i, outcome := range outcomes {
		if outcome.Status != "succeeded" {
			t.Fatalf("outcome %d: %+v", i, outcome)
		}
		value := <-seen
		if value.ID != outcome.Invocation.ID || value.Sequence != i+1 {
			t.Fatalf("delivery order %+v", value)
		}
	}
	if outcomes[0].Invocation.Trigger.Processed != 1 || outcomes[1].Invocation.Trigger.Processed != 2 {
		t.Fatalf("snapshots not immutable: %+v", outcomes)
	}
	if err := q.Prune(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.FindBatch(t.Context(), id); err != nil {
		t.Fatal("pruned named workflow authority", err)
	}
}

// TestNamedContinuationsRejectInvalidBindings verifies rejection occurs before creating workflow state.
func TestNamedContinuationsRejectInvalidBindings(t *testing.T) {
	q, err := NewSync(WithResourceID("continuation-test"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []*ContinuationBuilder{
		Continue(nil).OnSuccess(NewJob("hook")),
		Continue(q.Chain(NewJob("business"))).OnProgress(NewJob("hook")),
		Continue(q.Chain(NewJob("business")).Finally(func(context.Context, ChainState) error { return nil })).OnCompletion(NewJob("hook")),
		Continue(q.Batch(NewJob("business"))).OnSuccess(NewJob("hook").Retry(-1)),
		Continue(q.Chain(NewJob("business"))),
		Continue(q.Chain(NewJob("business"))).OnSuccess(NewJob("hook").UniqueFor(time.Minute)),
	}
	for i, b := range cases {
		if id, err := b.Dispatch(t.Context()); err == nil || id != "" {
			t.Fatalf("case %d accepted: %s %v", i, id, err)
		}
	}
}

// TestFakeNamedContinuationsRecordDefinitionsSeparately preserves fake acceptance and ownership semantics.
func TestFakeNamedContinuationsRecordDefinitionsSeparately(t *testing.T) {
	fake := NewFake()
	id, err := Continue(fake.Batch(NewJob("business"))).OnProgress(NewJob("progress").Payload([]byte("original"))).OnCompletion(NewJob("completion")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	records := fake.ContinuationRecords()
	if len(records) != 1 || records[0].WorkflowID != id || len(records[0].Definitions) != 2 {
		t.Fatalf("records %+v", records)
	}
	records[0].Definitions[0].Job.Payload[0] = 'X'
	if string(fake.ContinuationRecords()[0].Definitions[0].Job.Payload) != "original" {
		t.Fatal("fake catalog shares mutable payload")
	}
	outcomes, err := fake.FindContinuations(t.Context(), id)
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("untriggered definitions became invocations %+v %v", outcomes, err)
	}
	fake.Reset()
	if len(fake.ContinuationRecords()) != 0 {
		t.Fatal("reset retained continuation definitions")
	}
}

// TestSyncNamedContinuationPanicDoesNotStopLaterHooks protects the publisher-owned synchronous callback boundary.
func TestSyncNamedContinuationPanicDoesNotStopLaterHooks(t *testing.T) {
	q, err := NewSync(WithResourceID("panic-hooks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	q.Register("business", func(context.Context, Message) error { return nil })
	q.Register("panic", func(context.Context, Message) error { panic("private panic data") })
	q.Register("later", func(context.Context, Message) error { return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := Continue(q.Chain(NewJob("business"))).OnSuccess(NewJob("panic")).OnCompletion(NewJob("later")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	outcomes := awaitContinuations(t, q, id, 2)
	if outcomes[0].Status != "exhausted" || outcomes[1].Status != "succeeded" {
		t.Fatalf("publisher stopped after panic: %+v", outcomes)
	}
}

// TestNamedContinuationShutdownPreservesAcceptedContext prevents publisher cancellation from aborting accepted work.
func TestNamedContinuationShutdownPreservesAcceptedContext(t *testing.T) {
	q, err := NewWorkerpool(WithWorkers(2), WithResourceID("shutdown-hooks"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan context.Context, 1)
	finish := make(chan struct{})
	q.Register("business", func(context.Context, Message) error { return nil })
	q.Register("hook", func(ctx context.Context, _ Message) error { entered <- ctx; <-finish; return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := Continue(q.Chain(NewJob("business"))).OnSuccess(NewJob("hook")).Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	var execution context.Context
	select {
	case execution = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("hook never entered")
	}
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := q.Shutdown(shutdownCtx); err == nil {
		t.Fatal("shutdown released active callback")
	}
	if err := execution.Err(); err != nil {
		t.Fatalf("publisher cancellation reached accepted hook: %v", err)
	}
	close(finish)
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestNamedContinuationsRejectEncodedCatalogOverflow includes escaping and resolved routes before acceptance.
func TestNamedContinuationsRejectEncodedCatalogOverflow(t *testing.T) {
	for _, scenario := range []string{"explicit queue", "default queue", "escaped type", "default multiplication"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := Config{Driver: DriverWorkerpool}
			if scenario == "default queue" {
				cfg.DefaultQueue = strings.Repeat("q", 300<<10)
			}
			if scenario == "default multiplication" {
				cfg.DefaultQueue = strings.Repeat("q", 100<<10)
			}
			q, err := New(cfg, WithResourceID("scope"))
			if err != nil {
				t.Fatal(err)
			}
			defer q.Shutdown(context.Background())
			hook := NewJob("hook")
			switch scenario {
			case "explicit queue":
				hook = hook.OnQueue(strings.Repeat("q", 300<<10))
			case "escaped type":
				hook = NewJob(strings.Repeat("\x01", 30<<10))
			}
			bound := Continue(q.Chain(NewJob("business"))).OnCompletion(hook)
			if scenario == "default multiplication" {
				jobs := make([]Job, 256)
				for i := range jobs {
					jobs[i] = NewJob("business")
				}
				bound = Continue(q.Batch(jobs...)).OnProgress(hook)
			}
			if id, err := bound.Dispatch(t.Context()); err == nil || id != "" {
				t.Fatalf("oversized encoded catalog accepted: %s %v", id, err)
			}
		})
	}
}

// TestNamedContinuationsRejectLossyIdentifiers prevents accepted state from differing from its serialized claims.
func TestNamedContinuationsRejectLossyIdentifiers(t *testing.T) {
	for _, scenario := range []string{"hook type", "hook queue", "resource", "default queue", "member type", "member queue", "workflow queue", "batch name"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := Config{Driver: DriverWorkerpool}
			resource := "scope"
			if scenario == "default queue" {
				cfg.DefaultQueue = "queue\xff"
			}
			if scenario == "resource" {
				resource = "resource\xff"
			}
			q, err := New(cfg, WithResourceID(resource))
			if err != nil {
				t.Fatal(err)
			}
			defer q.Shutdown(context.Background())
			member, hook := NewJob("business"), NewJob("hook")
			switch scenario {
			case "hook type":
				hook = NewJob("hook\xff")
			case "hook queue":
				hook = hook.OnQueue("queue\xff")
			case "member type":
				member = NewJob("business\xff")
			case "member queue":
				member = member.OnQueue("queue\xff")
			}
			chain := q.Chain(member)
			if scenario == "workflow queue" {
				chain = chain.OnQueue("queue\xff")
			}
			bound := Continue(chain).OnCompletion(hook)
			if scenario == "batch name" {
				bound = Continue(q.Batch(member).Name("batch\xff")).OnCompletion(hook)
			}
			if id, err := bound.Dispatch(t.Context()); err == nil || id != "" {
				t.Fatalf("lossy named identity accepted: %q %v", id, err)
			}
		})
	}
}
