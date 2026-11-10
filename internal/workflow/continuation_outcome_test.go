package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

// deliverContinuationFixture runs the production continuation handler from actual store publication authority.
func deliverContinuationFixture(t *testing.T, r *runtime, store ContinuationStore, at time.Time, index int) {
	t.Helper()
	claim, ok, err := store.ClaimContinuation(t.Context(), "resource", fmt.Sprintf("publisher-%d", index), at, time.Minute)
	if err != nil || !ok {
		t.Fatal("continuation was not eligible", ok, err)
	}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.handleContinuation(t.Context(), testInboundJob{payload: raw}); err != nil {
		t.Fatal(err)
	}
}

// TestContinuationInfrastructureRedeliveryPreservesLogicalAttempts separates infrastructure redelivery from bounded application retries.
func TestContinuationInfrastructureRedeliveryPreservesLogicalAttempts(t *testing.T) {
	uncommitted := busruntime.Uncommitted(errors.New("temporary infrastructure failure"))
	retryable := errors.New("application retry")
	cases := []struct {
		name     string
		retry    int
		outcomes []error
		attempts []int
		status   string
	}{
		{"no retries", 0, []error{uncommitted, nil}, []int{0, 0}, "succeeded"},
		{"repeated infrastructure", 0, []error{uncommitted, uncommitted, nil}, []int{0, 0, 0}, "succeeded"},
		{"mixed outcomes", 1, []error{uncommitted, retryable, uncommitted, nil}, []int{0, 0, 1, 1}, "succeeded"},
		{"bounded exhaustion", 1, []error{uncommitted, retryable, retryable}, []int{0, 0, 1}, "exhausted"},
		{"permanent failure", 4, []error{uncommitted, busruntime.Permanent(retryable)}, []int{0, 0}, "exhausted"},
	}
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			if err := capable.PrepareContinuations(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					record := namedChainRecord(test.name)
					record.continuation.Definitions = record.continuation.Definitions[:1]
					record.continuation.Definitions[0].Job.Options.Retry = test.retry
					if err := store.CreateChain(t.Context(), record); err != nil {
						t.Fatal(err)
					}
					if _, _, err := store.AdvanceChain(t.Context(), record.ChainID, "member"); err != nil {
						t.Fatal(err)
					}
					engine, err := NewWithStore(&failingDispatchQueue{}, store, WithContinuationResource("resource"))
					if err != nil {
						t.Fatal(err)
					}
					r := engine.(*runtime)
					var attempts []int
					r.Register("first", func(ctx context.Context, _ Context) error {
						attempt, _ := busruntime.DeliveryAttemptFromContext(ctx)
						index := len(attempts)
						attempts = append(attempts, attempt.Number)
						if index >= len(test.outcomes) {
							t.Fatal("application retry budget was exceeded")
						}
						return test.outcomes[index]
					})
					for i := range test.outcomes {
						deliverContinuationFixture(t, r, capable, time.Now().Add(time.Second), i)
					}
					outcomes, err := capable.ContinuationOutcomes(t.Context(), record.ChainID)
					if err != nil || len(outcomes) != 1 || outcomes[0].Status != test.status || !reflect.DeepEqual(attempts, test.attempts) {
						t.Fatal("logical retry accounting changed", attempts, outcomes, err)
					}
				})
			}
		})
	}
}

// TestContinuationBackoffUsesLeaseClock keeps configured event timestamps separate from durable retry eligibility.
func TestContinuationBackoffUsesLeaseClock(t *testing.T) {
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			if err := capable.PrepareContinuations(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, clock := range []time.Time{time.Unix(0, 0), time.Now().Add(24 * time.Hour)} {
				id := fmt.Sprint(clock.UnixNano())
				record := namedChainRecord(id)
				record.continuation.Definitions = record.continuation.Definitions[:1]
				record.continuation.Definitions[0].Job.Options.Retry = 1
				record.continuation.Definitions[0].Job.Options.Backoff = time.Hour
				if err := store.CreateChain(t.Context(), record); err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.AdvanceChain(t.Context(), id, "member"); err != nil {
					t.Fatal(err)
				}
				engine, err := NewWithStore(&failingDispatchQueue{}, store, WithContinuationResource("resource"), WithClock(func() time.Time { return clock }))
				if err != nil {
					t.Fatal(err)
				}
				r := engine.(*runtime)
				calls := 0
				r.Register("first", func(context.Context, Context) error {
					calls++
					if calls == 1 {
						return errors.New("application retry")
					}
					return nil
				})
				before := time.Now()
				deliverContinuationFixture(t, r, capable, before, 0)
				if _, ok, err := capable.ClaimContinuation(t.Context(), "resource", "early", before.Add(time.Second), time.Minute); err != nil || ok {
					t.Fatal("event clock bypassed backoff", clock, ok, err)
				}
				later := time.Now().Add(time.Hour + time.Second)
				publication, ok, err := capable.ClaimContinuation(t.Context(), "resource", "later", later, time.Minute)
				if err != nil || !ok {
					t.Fatal("event clock postponed eligible retry", clock, ok, err)
				}
				execution, ok, err := capable.BeginContinuation(t.Context(), publication, "later-executor", later)
				if err != nil || !ok || execution.Attempt != 1 || calls != 1 {
					t.Fatal("retry authority changed", execution, calls, ok, err)
				}
				if err := capable.SettleContinuation(t.Context(), execution, "succeeded", clock); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestContinuationRedeliverySettlementIsFencedAndIdempotent prevents duplicate refunds or publication-only claims from granting retries.
func TestContinuationRedeliverySettlementIsFencedAndIdempotent(t *testing.T) {
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			if err := capable.PrepareContinuations(t.Context()); err != nil {
				t.Fatal(err)
			}
			record := namedChainRecord("redelivery")
			record.continuation.Definitions = record.continuation.Definitions[:1]
			if err := store.CreateChain(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AdvanceChain(t.Context(), record.ChainID, "member"); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			publication, ok, err := capable.ClaimContinuation(t.Context(), "resource", "publisher", now, time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if err := capable.SettleContinuation(t.Context(), publication, "redeliver", now); !errors.Is(err, ErrContinuationProtocol) {
				t.Fatal("publisher refunded an unallocated attempt", err)
			}
			execution, ok, err := capable.BeginContinuation(t.Context(), publication, "executor", now)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			for range 2 {
				if err := capable.SettleContinuation(t.Context(), execution, "redeliver", now); err != nil {
					t.Fatal("settlement receipt lost idempotence", err)
				}
			}
			next, ok, err := capable.ClaimContinuation(t.Context(), "resource", "next-publisher", now.Add(time.Second), time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			recovered, ok, err := capable.BeginContinuation(t.Context(), next, "new-executor", now.Add(time.Second))
			if err != nil || !ok || recovered.Attempt != 0 || recovered.Generation <= execution.Generation {
				t.Fatal("refund changed logical attempt or reused authority", recovered, ok, err)
			}
			if err := capable.SettleContinuation(t.Context(), execution, "succeeded", now); err == nil {
				t.Fatal("stale executor settled the redelivered attempt")
			}
		})
	}
}

// TestUnknownContinuationSettlementConsumesReservedAttempt preserves bounded crash accounting when no redelivery disposition committed.
func TestUnknownContinuationSettlementConsumesReservedAttempt(t *testing.T) {
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			if err := capable.PrepareContinuations(t.Context()); err != nil {
				t.Fatal(err)
			}
			record := namedChainRecord("unknown-outcome")
			record.continuation.Definitions = record.continuation.Definitions[:1]
			if err := store.CreateChain(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AdvanceChain(t.Context(), record.ChainID, "member"); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			publication, ok, err := capable.ClaimContinuation(t.Context(), "resource", "publisher", now, time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			execution, ok, err := capable.BeginContinuation(t.Context(), publication, "executor", now)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			later := now.Add(2 * time.Minute)
			recovery, ok, err := capable.ClaimContinuation(t.Context(), "resource", "replacement", later, time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if _, ok, err := capable.BeginContinuation(t.Context(), recovery, "new-executor", later); err != nil || ok {
				t.Fatal("unknown result invented an application retry refund", ok, err)
			}
			outcomes, err := capable.ContinuationOutcomes(t.Context(), record.ChainID)
			if err != nil || len(outcomes) != 1 || outcomes[0].Status != "exhausted" || outcomes[0].FailureCategory != "execution_interrupted" {
				t.Fatal("unknown result lost bounded interruption outcome", outcomes, err)
			}
			if err := capable.SettleContinuation(t.Context(), execution, "redeliver", later); err == nil {
				t.Fatal("expired owner refunded unknown execution")
			}
		})
	}
}
