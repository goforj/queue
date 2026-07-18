package queue

import (
	"context"
	"sync"
)

type dispatchAcceptanceContextKey struct{}

type dispatchAcceptance struct {
	mu        sync.Mutex
	accepted  bool
	callbacks []func()
}

// ensureDispatchAcceptance shares one acceptance boundary across observation, delivery, and orchestration adapters.
func ensureDispatchAcceptance(ctx context.Context) (context.Context, *dispatchAcceptance) {
	if ctx == nil {
		ctx = context.Background()
	}
	if current := dispatchAcceptanceFromContext(ctx); current != nil {
		return ctx, current
	}
	acceptance := &dispatchAcceptance{}
	return context.WithValue(ctx, dispatchAcceptanceContextKey{}, acceptance), acceptance
}

// newDispatchAcceptance starts an independent boundary so nested workflow dispatches cannot reuse their parent's accepted state.
func newDispatchAcceptance(ctx context.Context) (context.Context, *dispatchAcceptance) {
	if ctx == nil {
		ctx = context.Background()
	}
	acceptance := &dispatchAcceptance{}
	return context.WithValue(ctx, dispatchAcceptanceContextKey{}, acceptance), acceptance
}

// dispatchAcceptanceFromContext returns the current dispatch boundary when one has been installed.
func dispatchAcceptanceFromContext(ctx context.Context) *dispatchAcceptance {
	if ctx == nil {
		return nil
	}
	acceptance, _ := ctx.Value(dispatchAcceptanceContextKey{}).(*dispatchAcceptance)
	return acceptance
}

// onAccepted registers work that must occur after acceptance and before an inline delivery may begin.
func (a *dispatchAcceptance) onAccepted(callback func()) {
	if a == nil || callback == nil {
		return
	}
	a.mu.Lock()
	if !a.accepted {
		a.callbacks = append(a.callbacks, callback)
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	callback()
}

// markAccepted commits the dispatch boundary exactly once before releasing inline delivery gates.
func (a *dispatchAcceptance) markAccepted() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.accepted {
		a.mu.Unlock()
		return
	}
	a.accepted = true
	callbacks := append([]func(){}, a.callbacks...)
	a.callbacks = nil
	a.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

// isAccepted reports whether the delivery backend crossed its acceptance boundary.
func (a *dispatchAcceptance) isAccepted() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accepted
}

type acceptedExecutionError struct {
	cause error
}

// Error preserves the application execution error text returned by synchronous dispatch.
func (e acceptedExecutionError) Error() string {
	return e.cause.Error()
}

// Unwrap preserves errors.Is and errors.As behavior for the application execution failure.
func (e acceptedExecutionError) Unwrap() error {
	return e.cause
}

// DispatchAccepted reports that enqueue acceptance preceded the synchronous execution failure.
func (e acceptedExecutionError) DispatchAccepted() bool {
	return true
}
