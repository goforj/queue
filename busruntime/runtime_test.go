package busruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestDeliveryAttemptContext verifies attempt metadata remains typed and nil-context safe.
func TestDeliveryAttemptContext(t *testing.T) {
	if _, ok := DeliveryAttemptFromContext(nil); ok {
		t.Fatal("nil context unexpectedly contained an attempt")
	}

	want := DeliveryAttempt{Number: 2, MaxRetry: 4}
	ctx := WithDeliveryAttempt(nil, want)
	got, ok := DeliveryAttemptFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("delivery attempt = %+v, %t; want %+v, true", got, ok, want)
	}
	if _, ok := DeliveryAttemptFromContext(context.Background()); ok {
		t.Fatal("plain context unexpectedly contained an attempt")
	}
}

// TestDeliveryMetadataContext verifies direct correlation remains typed and
// independent from the physical attempt context.
func TestDeliveryMetadataContext(t *testing.T) {
	if _, ok := DeliveryMetadataFromContext(nil); ok {
		t.Fatal("nil context unexpectedly contained delivery metadata")
	}
	want := DeliveryMetadata{
		SchemaVersion: DeliveryMetadataVersion,
		DispatchID:    "dsp_1",
		JobID:         "job_1",
		Queue:         "critical",
	}
	ctx := WithDeliveryMetadata(nil, want)
	got, ok := DeliveryMetadataFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("delivery metadata = %+v, %t; want %+v, true", got, ok, want)
	}
	if _, ok := DeliveryAttemptFromContext(ctx); ok {
		t.Fatal("delivery metadata unexpectedly invented an attempt")
	}
	future := WithDeliveryMetadata(ctx, DeliveryMetadata{
		SchemaVersion: DeliveryMetadataVersion + 1,
		DispatchID:    "spoofed",
	})
	if metadata, ok := DeliveryMetadataFromContext(future); ok || metadata != (DeliveryMetadata{}) {
		t.Fatalf("future metadata = %+v, %t; want zero, false", metadata, ok)
	}
}

// TestContinuationScope verifies drain permission is runtime-owned, explicit, expiring, and nil-context safe.
func TestContinuationScope(t *testing.T) {
	first := NewContinuationScope()
	second := NewContinuationScope()
	if first.Owns(nil) || first.Owns(context.Background()) {
		t.Fatal("unmarked context unexpectedly belonged to a continuation scope")
	}
	ctx, release := first.Permit(nil)
	if !first.Owns(ctx) || second.Owns(ctx) {
		t.Fatal("marked context did not preserve scoped continuation ownership")
	}
	release()
	if first.Owns(ctx) {
		t.Fatal("released or escaped context retained continuation permission")
	}
}

// TestPreserveDeliveryContext verifies a replacement retains driver-owned
// delivery state while using its own cancellation, deadline, and user values.
func TestPreserveDeliveryContext(t *testing.T) {
	type contextKey struct{}
	key := contextKey{}
	first := NewContinuationScope()
	second := NewContinuationScope()
	replacementScope := NewContinuationScope()
	wantProvenance := DeliveryProvenance{GenerationID: "generation-source", RecoveredGenerationID: "generation-old", Recovered: true}
	wantAttempt := DeliveryAttempt{Number: 2, MaxRetry: 4}
	wantMetadata := DeliveryMetadata{
		SchemaVersion: DeliveryMetadataVersion,
		DispatchID:    "dispatch-source",
		JobID:         "job-source",
		ChainID:       "chain-source",
		Queue:         "critical",
	}

	source, cancelSource := context.WithCancel(context.WithValue(context.Background(), key, "source"))
	cancelSource()
	source, _ = WithDeliverySettlement(source)
	sourceIdentity, sourceIdentityOK := DeliverySettlementIdentityFromContext(source)
	if !sourceIdentityOK {
		t.Fatal("source context did not retain its settlement identity")
	}
	source = WithDeliveryProvenance(source, wantProvenance)
	source = WithDeliveryAttempt(source, wantAttempt)
	source = WithDeliveryMetadata(source, wantMetadata)
	source, releaseFirst := first.Permit(source)
	source, releaseSecond := second.Permit(source)
	replacementBase, cancelReplacement := context.WithTimeout(context.WithValue(context.Background(), key, "replacement"), time.Hour)
	replacement, _ := WithDeliverySettlement(replacementBase)
	replacementIdentity, replacementIdentityOK := DeliverySettlementIdentityFromContext(replacement)
	if !replacementIdentityOK || replacementIdentity == sourceIdentity {
		t.Fatal("replacement context did not begin with an independent settlement")
	}
	replacement = WithDeliveryProvenance(replacement, DeliveryProvenance{GenerationID: "spoofed"})
	replacement = WithDeliveryAttempt(replacement, DeliveryAttempt{Number: 99, MaxRetry: 99})
	replacement = WithDeliveryMetadata(replacement, DeliveryMetadata{SchemaVersion: DeliveryMetadataVersion, DispatchID: "spoofed"})
	replacement, releaseReplacement := replacementScope.Permit(replacement)
	preserved := PreserveDeliveryContext(source, replacement)

	if !first.Owns(preserved) || !second.Owns(preserved) || !replacementScope.Owns(preserved) {
		t.Fatal("replacement context did not retain every live continuation permit")
	}
	if got := preserved.Value(key); got != "replacement" {
		t.Fatalf("replacement context value = %v, want replacement", got)
	}
	if err := preserved.Err(); err != nil {
		t.Fatalf("preserved context inherited source cancellation: %v", err)
	}
	if got, ok := DeliverySettlementIdentityFromContext(preserved); !ok || got != sourceIdentity {
		t.Fatalf("settlement identity = %+v, %t; want source identity", got, ok)
	}
	if got, ok := DeliveryProvenanceFromContext(preserved); !ok || got != wantProvenance {
		t.Fatalf("delivery provenance = %+v, %t; want %+v", got, ok, wantProvenance)
	}
	if got, ok := DeliveryAttemptFromContext(preserved); !ok || got != wantAttempt {
		t.Fatalf("delivery attempt = %+v, %t; want %+v", got, ok, wantAttempt)
	}
	if got, ok := DeliveryMetadataFromContext(preserved); !ok || got != wantMetadata {
		t.Fatalf("delivery metadata = %+v, %t; want %+v", got, ok, wantMetadata)
	}
	wantDeadline, wantDeadlineOK := replacement.Deadline()
	if gotDeadline, ok := preserved.Deadline(); ok != wantDeadlineOK || !gotDeadline.Equal(wantDeadline) {
		t.Fatalf("replacement deadline = %v, %t; want %v, %t", gotDeadline, ok, wantDeadline, wantDeadlineOK)
	}

	derived := context.WithValue(source, key, "derived")
	derived = PreserveDeliveryContext(source, derived)
	if !first.Owns(derived) || !second.Owns(derived) || derived.Value(key) != "derived" {
		t.Fatal("source-derived replacement lost continuation authority or its replacement value")
	}
	releaseFirst()
	releaseSecond()
	if first.Owns(preserved) || second.Owns(preserved) {
		t.Fatal("preserved continuation permits survived handler return")
	}
	if !replacementScope.Owns(preserved) {
		t.Fatal("preserving source permits expired replacement-owned authority")
	}
	releaseReplacement()
	if replacementScope.Owns(preserved) {
		t.Fatal("replacement-owned permit survived its release")
	}
	cancelReplacement()
	if !errors.Is(preserved.Err(), context.Canceled) {
		t.Fatalf("preserved cancellation = %v, want replacement cancellation", preserved.Err())
	}

	plain := context.WithValue(context.Background(), key, "plain")
	if got := PreserveDeliveryContext(context.Background(), plain); got != plain {
		t.Fatal("permit-free source unnecessarily wrapped the replacement")
	}
	if got := PreserveDeliveryContext(nil, nil); got == nil || got.Err() != nil {
		t.Fatalf("nil source and replacement produced invalid context: %v", got)
	}
	futureSource := WithDeliveryMetadata(context.Background(), DeliveryMetadata{
		SchemaVersion: DeliveryMetadataVersion + 1,
		DispatchID:    "future-source",
	})
	trustedReplacement := WithDeliveryMetadata(context.Background(), DeliveryMetadata{
		SchemaVersion: DeliveryMetadataVersion,
		DispatchID:    "trusted-replacement",
	})
	if metadata, ok := DeliveryMetadataFromContext(PreserveDeliveryContext(futureSource, trustedReplacement)); ok || metadata != (DeliveryMetadata{}) {
		t.Fatalf("future source metadata became trusted replacement metadata: %+v, %t", metadata, ok)
	}
	permitOnly, releasePermitOnly := first.Permit(context.WithValue(context.Background(), key, "source-only"))
	withoutReplacement := PreserveDeliveryContext(permitOnly, nil)
	if !first.Owns(withoutReplacement) || withoutReplacement.Value(key) != nil {
		t.Fatal("nil replacement did not preserve only runtime-owned source state")
	}
	releasePermitOnly()
}

// TestDeliveryAttemptExhausted verifies MaxRetry counts retries after the initial attempt.
func TestDeliveryAttemptExhausted(t *testing.T) {
	tests := []struct {
		name      string
		attempt   DeliveryAttempt
		exhausted bool
	}{
		{name: "initial with retries remaining", attempt: DeliveryAttempt{Number: 0, MaxRetry: 2}, exhausted: false},
		{name: "final configured retry", attempt: DeliveryAttempt{Number: 2, MaxRetry: 2}, exhausted: true},
		{name: "no retries configured", attempt: DeliveryAttempt{Number: 0, MaxRetry: 0}, exhausted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.attempt.Exhausted(); got != test.exhausted {
				t.Fatalf("Exhausted() = %t, want %t", got, test.exhausted)
			}
		})
	}
}

// TestClassifyAttempt verifies application outcomes and infrastructure redelivery remain distinct.
func TestClassifyAttempt(t *testing.T) {
	applicationErr := errors.New("application failed")
	infrastructureErr := errors.New("store unavailable")
	tests := []struct {
		name    string
		attempt DeliveryAttempt
		err     error
		want    AttemptDecision
	}{
		{name: "success", attempt: DeliveryAttempt{Number: 0, MaxRetry: 2}, want: AttemptSucceeded},
		{name: "retryable", attempt: DeliveryAttempt{Number: 0, MaxRetry: 2}, err: applicationErr, want: AttemptRetry},
		{name: "exhausted", attempt: DeliveryAttempt{Number: 2, MaxRetry: 2}, err: applicationErr, want: AttemptFailed},
		{name: "permanent", attempt: DeliveryAttempt{Number: 0, MaxRetry: 2}, err: Permanent(applicationErr), want: AttemptFailed},
		{name: "uncommitted", attempt: DeliveryAttempt{Number: 2, MaxRetry: 2}, err: Uncommitted(infrastructureErr), want: AttemptRedeliver},
		{name: "wrapped permanent", attempt: DeliveryAttempt{Number: 0, MaxRetry: 2}, err: fmt.Errorf("middleware: %w", Permanent(applicationErr)), want: AttemptFailed},
		{name: "wrapped uncommitted", attempt: DeliveryAttempt{Number: 2, MaxRetry: 2}, err: fmt.Errorf("workflow: %w", Uncommitted(infrastructureErr)), want: AttemptRedeliver},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyAttempt(test.attempt, test.err); got != test.want {
				t.Fatalf("ClassifyAttempt() = %v, want %v", got, test.want)
			}
		})
	}
}

// TestAttemptErrorMarkers verifies markers are nil-safe, idempotent, and preserve error identity.
func TestAttemptErrorMarkers(t *testing.T) {
	cause := errors.New("cause")
	if Permanent(nil) != nil || Uncommitted(nil) != nil {
		t.Fatal("nil marker input must stay nil")
	}

	permanent := Permanent(cause)
	if !IsPermanent(permanent) || IsUncommitted(permanent) || !errors.Is(permanent, cause) {
		t.Fatalf("invalid permanent marker: %v", permanent)
	}
	if Permanent(permanent) != permanent {
		t.Fatal("Permanent must be idempotent")
	}

	uncommitted := Uncommitted(cause)
	if !IsUncommitted(uncommitted) || IsPermanent(uncommitted) || !errors.Is(uncommitted, cause) {
		t.Fatalf("invalid uncommitted marker: %v", uncommitted)
	}
	if Uncommitted(uncommitted) != uncommitted {
		t.Fatal("Uncommitted must be idempotent")
	}
}
