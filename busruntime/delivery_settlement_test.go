package busruntime

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestDeliverySettlementDefersAndCommitsOnce verifies settlement facts cannot precede the driver's positive commit.
func TestDeliverySettlementDefersAndCommitsOnce(t *testing.T) {
	ctx, settlement := WithDeliverySettlement(context.Background())
	identity, ok := DeliverySettlementIdentityFromContext(ctx)
	if !ok {
		t.Fatal("settlement identity was absent from its context")
	}
	if same, sameOK := DeliverySettlementIdentityFromContext(ctx); !sameOK || same != identity {
		t.Fatal("settlement identity changed for the same context")
	}
	otherCtx, _ := WithDeliverySettlement(context.Background())
	if other, otherOK := DeliverySettlementIdentityFromContext(otherCtx); !otherOK || other == identity {
		t.Fatal("distinct settlements shared one identity")
	}
	var calls atomic.Int32
	if !DeferUntilDeliveryCommitted(ctx, func() { calls.Add(1) }) {
		t.Fatal("settlement callback was not deferred")
	}
	if calls.Load() != 0 {
		t.Fatal("settlement callback ran before commit")
	}
	settlement.Commit()
	settlement.Commit()
	if calls.Load() != 1 {
		t.Fatalf("settlement callback calls = %d, want 1", calls.Load())
	}
}

// TestDeliverySettlementLateRegistration verifies facts registered after a commit still observe that committed boundary.
func TestDeliverySettlementLateRegistration(t *testing.T) {
	ctx, settlement := WithDeliverySettlement(nil)
	settlement.Commit()
	called := false
	if !DeferUntilDeliveryCommitted(ctx, func() { called = true }) {
		t.Fatal("late settlement callback was not recognized")
	}
	if !called {
		t.Fatal("late settlement callback did not run")
	}
}

// TestDeliverySettlementAbsentAndPanickingCallbacks verifies optional settlement and telemetry panics remain isolated.
func TestDeliverySettlementAbsentAndPanickingCallbacks(t *testing.T) {
	if identity, ok := DeliverySettlementIdentityFromContext(nil); ok || identity != (DeliverySettlementIdentity{}) {
		t.Fatalf("nil context identity = %#v ok:%t, want zero/false", identity, ok)
	}
	if identity, ok := DeliverySettlementIdentityFromContext(context.Background()); ok || identity != (DeliverySettlementIdentity{}) {
		t.Fatalf("plain context identity = %#v ok:%t, want zero/false", identity, ok)
	}
	if DeferUntilDeliveryCommitted(context.Background(), func() {}) {
		t.Fatal("plain context unexpectedly exposed settlement")
	}
	ctx, settlement := WithDeliverySettlement(context.Background())
	var after atomic.Bool
	DeferUntilDeliveryCommitted(ctx, func() { panic("observer failed") })
	DeferUntilDeliveryCommitted(ctx, func() { after.Store(true) })
	settlement.Commit()
	if !after.Load() {
		t.Fatal("panicking callback prevented later settlement facts")
	}
}

// TestDeliveryProvenanceContext distinguishes current generation identity from
// an earlier delivery whose settlement owner proved it remained unsettled.
func TestDeliveryProvenanceContext(t *testing.T) {
	if _, ok := DeliveryProvenanceFromContext(nil); ok {
		t.Fatal("nil context unexpectedly reports delivery provenance")
	}
	if _, ok := DeliveryProvenanceFromContext(context.Background()); ok {
		t.Fatal("plain context unexpectedly reports delivery provenance")
	}
	want := DeliveryProvenance{
		GenerationID:          "generation-current",
		RecoveredGenerationID: "generation-earlier",
		Recovered:             true,
	}
	ctx := WithDeliveryProvenance(nil, want)
	if got, ok := DeliveryProvenanceFromContext(ctx); !ok || got != want {
		t.Fatalf("delivery provenance = %+v ok:%t, want %+v/true", got, ok, want)
	}
}

// TestDeliveryApplicationStateCommittedSignal keeps post-mutation provenance
// distinct from both deferred fact publication and physical settlement.
func TestDeliveryApplicationStateCommittedSignal(t *testing.T) {
	if MarkDeliveryApplicationStateCommitted(nil) {
		t.Fatal("nil context accepted an application-state signal")
	}
	if MarkDeliveryApplicationStateCommitted(context.Background()) {
		t.Fatal("plain context accepted an application-state signal")
	}
	ctx, settlement := WithDeliverySettlement(context.Background())
	if settlement.ApplicationStateCommitted() {
		t.Fatal("new settlement reports committed application state")
	}
	if !MarkDeliveryApplicationStateCommitted(ctx) || !settlement.ApplicationStateCommitted() {
		t.Fatal("settlement did not retain committed application state")
	}
	settlement.Commit()
	if !settlement.ApplicationStateCommitted() {
		t.Fatal("physical settlement erased application-state provenance")
	}
	var nilSettlement *DeliverySettlement
	if nilSettlement.ApplicationStateCommitted() {
		t.Fatal("nil settlement reports committed application state")
	}
}
