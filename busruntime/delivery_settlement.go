package busruntime

import (
	"context"
	"sync"
)

// DeliverySettlement defers facts that become true only after a worker commits broker settlement.
// Its zero value is ready for use, and Commit is idempotent.
type DeliverySettlement struct {
	mu        sync.Mutex
	committed bool
	callbacks []func()
}

type deliverySettlementContextKey struct{}

// WithDeliverySettlement attaches one settlement boundary to a handler context.
// Drivers that acknowledge or delete deliveries after handler execution use the returned value to commit deferred facts.
func WithDeliverySettlement(ctx context.Context) (context.Context, *DeliverySettlement) {
	if ctx == nil {
		ctx = context.Background()
	}
	settlement := &DeliverySettlement{}
	return context.WithValue(ctx, deliverySettlementContextKey{}, settlement), settlement
}

// DeferUntilDeliveryCommitted registers fn when ctx carries a driver-owned settlement boundary.
// It returns false when the driver cannot report settlement, allowing callers to preserve legacy immediate behavior.
func DeferUntilDeliveryCommitted(ctx context.Context, fn func()) bool {
	if ctx == nil || fn == nil {
		return false
	}
	settlement, ok := ctx.Value(deliverySettlementContextKey{}).(*DeliverySettlement)
	if !ok || settlement == nil {
		return false
	}
	settlement.deferCommit(fn)
	return true
}

// Commit publishes every deferred fact after the driver has positively settled the delivery.
func (s *DeliverySettlement) Commit() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.committed {
		s.mu.Unlock()
		return
	}
	s.committed = true
	callbacks := s.callbacks
	s.callbacks = nil
	s.mu.Unlock()

	for _, callback := range callbacks {
		invokeDeliveryCommit(callback)
	}
}

// deferCommit queues a callback or invokes it immediately when settlement already committed.
func (s *DeliverySettlement) deferCommit(fn func()) {
	s.mu.Lock()
	if !s.committed {
		s.callbacks = append(s.callbacks, fn)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	invokeDeliveryCommit(fn)
}

// invokeDeliveryCommit isolates deferred telemetry so it cannot invalidate an already committed broker acknowledgement.
func invokeDeliveryCommit(fn func()) {
	defer func() {
		_ = recover()
	}()
	fn()
}
