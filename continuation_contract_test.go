package queue

import (
	"errors"
	"testing"
	"time"
)

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
