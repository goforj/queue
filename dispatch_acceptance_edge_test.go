package queue

import (
	"errors"
	"testing"
)

// TestDispatchAcceptanceNilEdges verifies optional context and receiver boundaries remain safe.
func TestDispatchAcceptanceNilEdges(t *testing.T) {
	ctx, acceptance := ensureDispatchAcceptance(nil)
	if ctx == nil || acceptance == nil {
		t.Fatalf("ensureDispatchAcceptance(nil) = %v, %p; want initialized values", ctx, acceptance)
	}
	if resolved := dispatchAcceptanceFromContext(ctx); resolved != acceptance {
		t.Fatalf("resolved acceptance = %p, want %p", resolved, acceptance)
	}
	if resolved := dispatchAcceptanceFromContext(nil); resolved != nil {
		t.Fatalf("nil context resolved acceptance %p", resolved)
	}

	callbackCalled := false
	acceptance.onAccepted(nil)
	var absent *dispatchAcceptance
	absent.onAccepted(func() { callbackCalled = true })
	absent.markAccepted()
	if absent.isAccepted() {
		t.Fatal("nil acceptance reported accepted")
	}
	if callbackCalled {
		t.Fatal("nil acceptance invoked its callback")
	}
}

// TestAcceptedExecutionErrorPreservesCauseAndAcceptance verifies synchronous failures retain both error and settlement semantics.
func TestAcceptedExecutionErrorPreservesCauseAndAcceptance(t *testing.T) {
	cause := errors.New("handler failed")
	err := acceptedExecutionError{cause: cause}
	if err.Error() != cause.Error() {
		t.Fatalf("error text = %q, want %q", err.Error(), cause.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, %v) = false", err, cause)
	}
	if !err.DispatchAccepted() {
		t.Fatal("accepted execution error did not report dispatch acceptance")
	}

	var _ interface {
		DispatchAccepted() bool
	} = err
}
