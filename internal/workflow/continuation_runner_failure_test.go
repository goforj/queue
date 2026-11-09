package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

// continuationFaultStore separates infrastructure failure from the real memory ledger's execution authority.
type continuationFaultStore struct {
	Store
	ContinuationStore
	mode      string
	calls     int
	recovered chan struct{}
}

// ClaimContinuation injects bounded publisher faults before any physical delivery can be accepted.
func (s *continuationFaultStore) ClaimContinuation(ctx context.Context, resource, owner string, now time.Time, lease time.Duration) (continuationClaim, bool, error) {
	s.calls++
	switch s.mode {
	case "panic":
		panic("private publisher diagnostic")
	case "protocol":
		return continuationClaim{}, false, ErrContinuationProtocol
	case "permanent":
		return continuationClaim{}, false, busruntime.Permanent(errors.New("private authority diagnostic"))
	case "encoding":
		return continuationClaim{Intent: continuationIntent{Outcome: busruntime.ContinuationOutcome{CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}}, true, nil
	case "transient":
		if s.calls == 1 {
			return continuationClaim{}, false, errors.New("temporary store outage")
		}
		if s.calls == 2 {
			close(s.recovered)
		}
		return continuationClaim{}, false, nil
	}
	return s.ContinuationStore.ClaimContinuation(ctx, resource, owner, now, lease)
}

// BeginContinuation isolates definite protocol loss from a stale physical delivery that must be ignored.
func (s *continuationFaultStore) BeginContinuation(ctx context.Context, claim continuationClaim, owner string, at time.Time) (continuationClaim, bool, error) {
	if s.mode == "begin protocol" {
		return continuationClaim{}, false, ErrContinuationProtocol
	}
	if s.mode == "stale" {
		return continuationClaim{}, false, nil
	}
	return s.ContinuationStore.BeginContinuation(ctx, claim, owner, at)
}

// SettleContinuation exposes failure after the application effect so terminal observer facts must stay uncommitted.
func (s *continuationFaultStore) SettleContinuation(ctx context.Context, claim continuationClaim, status string, at time.Time) error {
	if s.mode == "settle protocol" {
		return ErrContinuationProtocol
	}
	return s.ContinuationStore.SettleContinuation(ctx, claim, status, at)
}

// TestContinuationPublisherFailureReachesLifecycle checks fatal classification, safe diagnostics and transient recovery without timing-only sleeps.
func TestContinuationPublisherFailureReachesLifecycle(t *testing.T) {
	for _, mode := range []string{"panic", "protocol", "permanent", "encoding", "transient"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryStore()
			fault := &continuationFaultStore{Store: store, ContinuationStore: store.(ContinuationStore), mode: mode, recovered: make(chan struct{})}
			engine, err := NewWithStore(&failingDispatchQueue{}, fault, WithContinuationResource("resource"))
			if err != nil {
				t.Fatal(err)
			}
			runtime := engine.(*runtime)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := runtime.StartCoordinator(ctx); err != nil {
				t.Fatal(err)
			}
			if err := runtime.StartCoordinator(ctx); err != nil {
				t.Fatal("duplicate coordinator startup failed", err)
			}
			if mode == "transient" {
				select {
				case <-fault.recovered:
				case <-ctx.Done():
					t.Fatal("publisher did not recover", ctx.Err())
				}
				if runtime.CoordinatorError() != nil {
					t.Fatal("transient recovery became fatal")
				}
			} else {
				select {
				case <-runtime.CoordinatorDone():
				case <-ctx.Done():
					t.Fatal("publisher failure did not reach lifecycle", ctx.Err())
				}
				if err := runtime.CoordinatorError(); err == nil || strings.Contains(err.Error(), "private") {
					t.Fatal("fatal result lost or leaked diagnostics", err)
				}
			}
			if err := runtime.StopCoordinator(ctx); err != nil {
				t.Fatal(err)
			}
			if err := runtime.StartCoordinator(ctx); err == nil {
				t.Fatal("stopped coordinator restarted")
			}
		})
	}
}

// legacyContinuationStore deliberately exposes only the established workflow store contract.
type legacyContinuationStore struct{ Store }

// TestContinuationDeliveryRejectsMalformedAndUnsupportedAuthority prevents invalid claims from entering logical handlers.
func TestContinuationDeliveryRejectsMalformedAndUnsupportedAuthority(t *testing.T) {
	for _, mode := range []string{"malformed", "version", "resource", "legacy store", "begin protocol", "stale", "valid"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryStore()
			fault := &continuationFaultStore{Store: store, ContinuationStore: store.(ContinuationStore), mode: mode}
			var selected Store = fault
			if mode == "legacy store" {
				selected = &legacyContinuationStore{Store: store}
			}
			engine, err := NewWithStore(&failingDispatchQueue{}, selected, WithContinuationResource("resource"))
			if err != nil {
				t.Fatal(err)
			}
			runtime := engine.(*runtime)
			calls := 0
			runtime.Register("first", func(context.Context, Context) error { calls++; return nil })
			capable := store.(ContinuationStore)
			if err := capable.PrepareContinuations(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateChain(t.Context(), namedChainRecord("parent")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AdvanceChain(t.Context(), "parent", "member"); err != nil {
				t.Fatal(err)
			}
			claim, ok, err := capable.ClaimContinuation(t.Context(), "resource", "publisher", time.Now(), time.Minute)
			if err != nil || !ok {
				t.Fatal("invalid control authority", ok, err)
			}
			if mode == "version" {
				claim.Version = 99
			}
			if mode == "resource" {
				claim.Resource = "other"
			}
			raw, err := json.Marshal(claim)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "malformed" {
				raw = []byte("{")
			}
			err = runtime.handleContinuation(t.Context(), testInboundJob{payload: raw})
			if mode == "valid" {
				if err != nil || calls != 1 {
					t.Fatal("valid authority did not invoke counted handler", calls, err)
				}
				return
			}
			if mode == "stale" {
				if err != nil {
					t.Fatal("stale physical claim was not ignored", err)
				}
			} else if mode == "begin protocol" {
				if !busruntime.IsUncommitted(err) {
					t.Fatal("authority outage became application failure", err)
				}
			} else if !busruntime.IsPermanent(err) {
				t.Fatal("invalid claim not rejected terminally", err)
			}
			if calls != 0 {
				t.Fatal("invalid authority entered application handler")
			}
		})
	}
}

// TestContinuationSettlementFailureDoesNotPublishTerminalSuccess protects observer truth when the application effect precedes durable settlement.
func TestContinuationSettlementFailureDoesNotPublishTerminalSuccess(t *testing.T) {
	store := NewMemoryStore()
	capable := store.(ContinuationStore)
	ctx := t.Context()
	if err := capable.PrepareContinuations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateChain(ctx, namedChainRecord("parent")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AdvanceChain(ctx, "parent", "member"); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := capable.ClaimContinuation(ctx, "resource", "publisher", time.Now(), time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	fault := &continuationFaultStore{Store: store, ContinuationStore: capable, mode: "settle protocol"}
	successes, calls := 0, 0
	engine, err := NewWithStore(&failingDispatchQueue{}, fault, WithContinuationResource("resource"), WithObserver(ObserverFunc(func(_ context.Context, event Event) {
		if event.Kind == EventJobSucceeded {
			successes++
		}
	})))
	if err != nil {
		t.Fatal(err)
	}
	runtime := engine.(*runtime)
	runtime.Register("first", func(context.Context, Context) error { calls++; return nil })
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.handleContinuation(ctx, testInboundJob{payload: raw}); !busruntime.IsUncommitted(err) {
		t.Fatal("settlement error was not retained as infrastructure uncertainty", err)
	}
	if calls != 1 || successes != 0 {
		t.Fatalf("effect/fact mismatch: executions=%d successes=%d", calls, successes)
	}
}
