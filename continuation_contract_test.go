package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

// TestNamedContinuationRedeliversUncommittedWithoutApplicationRetries protects the public handler and publisher path at Retry(0).
func TestNamedContinuationRedeliversUncommittedWithoutApplicationRetries(t *testing.T) {
	q, err := NewSync(WithResourceID("uncommitted-hook"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
	q.Register("business", func(context.Context, Message) error { return nil })
	var calls atomic.Int32
	attempts := make(chan int, 2)
	q.Register("hook", func(_ context.Context, message Message) error {
		attempts <- message.Attempt
		if calls.Add(1) == 1 {
			return busruntime.Uncommitted(errors.New("temporary infrastructure failure"))
		}
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := Continue(q.Chain(NewJob("business"))).OnCompletion(NewJob("hook").Retry(0)).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	outcomes := awaitContinuations(t, q, id, 1)
	if calls.Load() != 2 || outcomes[0].Status != "succeeded" {
		t.Fatal("infrastructure outcome consumed application retry budget", calls.Load(), outcomes)
	}
	for range 2 {
		if attempt := <-attempts; attempt != 0 {
			t.Fatal("infrastructure redelivery advanced application attempt", attempt)
		}
	}
}

// TestFakeContinuationAuthorityTracksExplicitTransitions preserves fake recording without inventing hook execution.
func TestFakeContinuationAuthorityTracksExplicitTransitions(t *testing.T) {
	fake := NewFake()
	ctx := t.Context()
	id, err := Continue(fake.Chain(NewJob("business"))).OnCompletion(NewJob("hook")).Dispatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recorder := fake.state.workflow
	state, err := recorder.GetChain(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := recorder.AdvanceChain(ctx, id, state.Nodes[0].NodeID); err != nil {
		t.Fatal(err)
	}
	outcomes, err := fake.FindContinuations(ctx, id)
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != "pending" {
		t.Fatal("explicit fake transition did not create obligation", outcomes, err)
	}
	claim, ok, err := recorder.ClaimContinuation(ctx, "fake", "publisher", time.Now(), time.Minute)
	if err != nil || !ok {
		t.Fatal("fake recovery claim failed", ok, err)
	}
	if valid, err := recorder.ValidateContinuation(ctx, claim); err != nil || !valid {
		t.Fatal("fake claim association lost", valid, err)
	}
	execution, ok, err := recorder.BeginContinuation(ctx, claim, "worker", time.Now())
	if err != nil || !ok {
		t.Fatal("fake execution authority lost", ok, err)
	}
	if err := recorder.RenewContinuation(ctx, execution, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := recorder.SettleContinuation(ctx, execution, "succeeded", time.Now()); err != nil {
		t.Fatal(err)
	}
	outcomes, err = fake.FindContinuations(ctx, id)
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != "succeeded" {
		t.Fatal("fake settlement not visible", outcomes, err)
	}
	fake.AssertNotDispatched(t, "hook")
}

// TestContinuationZeroAndNilBindingsRejectWithoutAcceptance protects application error paths without requiring a concrete backend execution.
func TestContinuationZeroAndNilBindingsRejectWithoutAcceptance(t *testing.T) {
	q, err := NewSync(WithResourceID("binding-errors"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(t.Context())
	var chain *chainBuilderAdapter
	var batch *batchBuilderAdapter
	cases := map[string]*ContinuationBuilder{
		"typed nil chain": Continue(chain), "typed nil batch": Continue(batch),
		"invalid chain job": Continue(q.Chain(NewJob(""))), "invalid batch job": Continue(q.Batch(NewJob(""))),
		"invalid hook": Continue(q.Chain(NewJob("business"))).OnCompletion(NewJob("")),
		"zero builder": {},
	}
	for name, builder := range cases {
		t.Run(name, func(t *testing.T) {
			if id, err := builder.Dispatch(t.Context()); err == nil || id != "" {
				t.Fatal("invalid binding accepted", id, err)
			}
		})
	}
	var absent *Queue
	if _, err := absent.FindContinuations(t.Context(), "id"); err == nil {
		t.Fatal("nil queue exposed continuation reader")
	}
	if _, err := (&Queue{}).FindContinuations(t.Context(), "id"); !errors.Is(err, ErrWorkflowUnsupported) {
		t.Fatal("unsupported resource exposed continuation reader", err)
	}
}

// TestRejectedFakeNamedWorkflowsRemoveOnlyRejectedAuthority prevents phantom obligations without erasing accepted sibling definitions.
func TestRejectedFakeNamedWorkflowsRemoveOnlyRejectedAuthority(t *testing.T) {
	for _, kind := range []string{"chain", "batch"} {
		t.Run(kind, func(t *testing.T) {
			fake := NewFake()
			ctx := t.Context()
			build := func() *ContinuationBuilder {
				if kind == "chain" {
					return Continue(fake.Chain(NewJob("business")))
				}
				return Continue(fake.Batch(NewJob("business")))
			}
			accepted, err := build().OnCompletion(NewJob("complete").Payload([]byte("sibling"))).Dispatch(ctx)
			if err != nil {
				t.Fatal(err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			rejected, err := build().OnFailure(NewJob("fail")).OnCompletion(NewJob("complete")).Dispatch(canceled)
			if err == nil || rejected == "" {
				t.Fatal("canceled dispatch did not reject with identity", rejected, err)
			}
			if kind == "chain" {
				if _, err := fake.FindChain(ctx, rejected); !errors.Is(err, ErrWorkflowNotFound) {
					t.Fatal("rejected chain remained", err)
				}
			} else {
				if _, err := fake.FindBatch(ctx, rejected); !errors.Is(err, ErrWorkflowNotFound) {
					t.Fatal("rejected batch remained", err)
				}
			}
			if outcomes, err := fake.FindContinuations(ctx, rejected); !errors.Is(err, ErrWorkflowNotFound) {
				t.Fatal("phantom rejected obligations", outcomes, err)
			}
			if outcomes, err := fake.FindContinuations(ctx, accepted); err != nil || len(outcomes) != 0 {
				t.Fatal("accepted sibling authority changed", outcomes, err)
			}
			records := fake.ContinuationRecords()
			if len(records) != 1 || records[0].WorkflowID != accepted || string(records[0].Definitions[0].Job.Payload) != "sibling" {
				t.Fatal("accepted sibling definitions changed", records)
			}
			if len(fake.state.workflow.continuations) != 1 {
				t.Fatal("rejected definitions retained internally")
			}
		})
	}
}
