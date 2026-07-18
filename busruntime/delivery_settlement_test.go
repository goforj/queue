package busruntime

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestDeliverySettlementDefersAndCommitsOnce verifies settlement facts cannot precede the driver's positive commit.
func TestDeliverySettlementDefersAndCommitsOnce(t *testing.T) {
	ctx, settlement := WithDeliverySettlement(context.Background())
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
