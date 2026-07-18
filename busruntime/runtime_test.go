package busruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"
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
