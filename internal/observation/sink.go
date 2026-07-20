// Package observation provides internal fan-out plumbing without depending on
// the queue package's public event model.
package observation

import (
	"context"
	"sync"
)

// Sink fans one typed value out to observers that may be added during runtime construction.
type Sink[T any] struct {
	mu        sync.RWMutex
	observers []func(context.Context, T)
}

// NewSink creates a mutable typed observer sink.
func NewSink[T any](observers ...func(context.Context, T)) *Sink[T] {
	sink := &Sink[T]{}
	for _, observer := range observers {
		sink.Add(observer)
	}
	return sink
}

// Add appends an observer while preserving registration order for each emitted value.
func (s *Sink[T]) Add(observer func(context.Context, T)) {
	if s == nil || observer == nil {
		return
	}
	s.mu.Lock()
	s.observers = append(s.observers, observer)
	s.mu.Unlock()
}

// HasObservers reports whether emitting through the sink can reach an observer.
func (s *Sink[T]) HasObservers() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	hasObservers := len(s.observers) > 0
	s.mu.RUnlock()
	return hasObservers
}

// Observe invokes a stable observer snapshot so observers may be added without blocking callbacks.
func (s *Sink[T]) Observe(ctx context.Context, value T) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	observers := s.snapshot()
	for _, observer := range observers {
		observe(ctx, observer, value)
	}
}

// snapshot avoids holding the sink lock while application callbacks execute.
func (s *Sink[T]) snapshot() []func(context.Context, T) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	observers := make([]func(context.Context, T), len(s.observers))
	copy(observers, s.observers)
	return observers
}

// observe isolates a panicking callback so later observers still receive the value.
func observe[T any](ctx context.Context, observer func(context.Context, T), value T) {
	defer func() {
		_ = recover()
	}()
	observer(ctx, value)
}
