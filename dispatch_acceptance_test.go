package queue

import (
	"context"
	"testing"
)

// TestDispatchAcceptanceSeparatesNestedDispatches verifies child workflows cannot inherit an accepted parent boundary.
func TestDispatchAcceptanceSeparatesNestedDispatches(t *testing.T) {
	parentCtx, parent := newDispatchAcceptance(context.Background())
	parent.markAccepted()

	childCtx, child := newDispatchAcceptance(parentCtx)
	if child == parent || child.isAccepted() {
		t.Fatalf("new child boundary reused accepted parent: parent=%p child=%p", parent, child)
	}
	resolvedCtx, resolved := ensureDispatchAcceptance(childCtx)
	if resolvedCtx != childCtx || resolved != child {
		t.Fatal("observer adapter did not reuse the current child boundary")
	}

	child.markAccepted()
	if !parent.isAccepted() || !child.isAccepted() {
		t.Fatal("accepting child changed either boundary unexpectedly")
	}
}

// TestDispatchAcceptanceCallbacksRunOnce verifies multiple marks cannot duplicate enqueue facts.
func TestDispatchAcceptanceCallbacksRunOnce(t *testing.T) {
	_, acceptance := newDispatchAcceptance(nil)
	calls := 0
	acceptance.onAccepted(func() { calls++ })
	acceptance.markAccepted()
	acceptance.markAccepted()
	acceptance.onAccepted(func() { calls++ })
	if calls != 2 {
		t.Fatalf("callback calls = %d, want one registered-before and one registered-after call", calls)
	}
}
