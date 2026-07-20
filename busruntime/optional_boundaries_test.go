package busruntime

import (
	"context"
	"testing"
)

// TestDeliverySettlementNilInputsRemainOptional verifies callers can retain
// immediate legacy behavior without manufacturing a settlement boundary.
func TestDeliverySettlementNilInputsRemainOptional(t *testing.T) {
	if DeferUntilDeliveryCommitted(nil, func() {}) {
		t.Fatal("nil context accepted a deferred delivery callback")
	}
	ctx, settlement := WithDeliverySettlement(context.Background())
	if DeferUntilDeliveryCommitted(ctx, nil) {
		t.Fatal("nil callback was reported as deferred")
	}
	settlement.Commit()

	var absent *DeliverySettlement
	absent.Commit()
}

// TestZeroContinuationScopeNeverAuthorizesDispatch proves nil and zero-value
// scopes preserve their documented no-permission behavior.
func TestZeroContinuationScopeNeverAuthorizesDispatch(t *testing.T) {
	var nilScope *ContinuationScope
	ctx, release := nilScope.Permit(nil)
	release()
	if ctx == nil || nilScope.Owns(ctx) {
		t.Fatal("nil continuation scope granted permission")
	}

	zeroScope := &ContinuationScope{}
	ctx, release = zeroScope.Permit(context.Background())
	release()
	if zeroScope.Owns(ctx) {
		t.Fatal("zero continuation scope granted permission")
	}
}
