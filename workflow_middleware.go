package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/queue/busruntime"
)

// Next invokes the remaining queue middleware and logical job handler.
// @group Queue
type Next func(ctx context.Context, message Message) error

// Middleware intercepts logical queue job execution.
// @group Queue
type Middleware interface {
	// Handle wraps the remaining middleware and handler chain.
	Handle(ctx context.Context, message Message, next Next) error
}

// MiddlewareFunc adapts a function to Middleware.
// @group Queue
type MiddlewareFunc func(ctx context.Context, message Message, next Next) error

// Handle calls the wrapped middleware function.
func (f MiddlewareFunc) Handle(ctx context.Context, message Message, next Next) error {
	return f(ctx, message, next)
}

var (
	// ErrSkipped identifies a job intentionally suppressed by middleware.
	ErrSkipped = errors.New("bus job skipped by middleware")
	// ErrRateLimited identifies a job rejected by its configured rate limiter.
	ErrRateLimited = errors.New("bus job rate limited")
	// ErrOverlapping identifies a job rejected because its execution key is already locked.
	ErrOverlapping = errors.New("bus job overlap prevented")
)

// RetryPolicy leaves retry ownership to the underlying worker runtime.
// @group Queue
type RetryPolicy struct{}

// Handle passes execution through without modification.
func (RetryPolicy) Handle(ctx context.Context, message Message, next Next) error {
	return next(ctx, message)
}

// SkipWhen suppresses handler execution when its predicate matches.
// @group Queue
type SkipWhen struct {
	Predicate func(ctx context.Context, message Message) bool
}

// Handle skips job execution when Predicate returns true.
func (s SkipWhen) Handle(ctx context.Context, message Message, next Next) error {
	if s.Predicate != nil && s.Predicate(ctx, message) {
		return nil
	}
	return next(ctx, message)
}

// FailOnError marks selected handler failures as permanent.
// @group Queue
type FailOnError struct {
	When func(err error) bool
}

// Handle wraps matched errors as fatal errors to stop retries.
func (f FailOnError) Handle(ctx context.Context, message Message, next Next) error {
	err := next(ctx, message)
	if err == nil {
		return nil
	}
	if f.When == nil || f.When(err) {
		return busruntime.Permanent(fmt.Errorf("fatal bus error: %w", err))
	}
	return err
}

// RateLimiter decides whether a logical key may execute now.
// @group Queue
type RateLimiter interface {
	// Allow returns whether key may execute and any suggested retry delay.
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

// RateLimit applies a RateLimiter before handler execution.
// @group Queue
type RateLimit struct {
	Key     func(ctx context.Context, message Message) string
	Limiter RateLimiter
}

// Handle applies limiter checks before executing the next handler.
func (r RateLimit) Handle(ctx context.Context, message Message, next Next) error {
	if r.Limiter == nil {
		return next(ctx, message)
	}
	key := message.JobType
	if r.Key != nil {
		if resolved := r.Key(ctx, message); resolved != "" {
			key = resolved
		}
	}
	allowed, _, err := r.Limiter.Allow(ctx, key)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrRateLimited
	}
	return next(ctx, message)
}

// Lock represents an acquired overlap-prevention lease.
// @group Queue
type Lock interface {
	// Release relinquishes the acquired lease.
	Release(ctx context.Context) error
}

// Locker acquires keyed leases for overlap prevention.
// @group Queue
type Locker interface {
	// Acquire attempts to hold key for ttl.
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, bool, error)
}

// WithoutOverlapping serializes executions that resolve to the same key.
// @group Queue
type WithoutOverlapping struct {
	Key    func(ctx context.Context, message Message) string
	TTL    time.Duration
	Locker Locker
}

// Handle acquires a lock and prevents concurrent overlap for the same key.
func (w WithoutOverlapping) Handle(ctx context.Context, message Message, next Next) error {
	if w.Locker == nil {
		return next(ctx, message)
	}
	key := message.JobType
	if w.Key != nil {
		if resolved := w.Key(ctx, message); resolved != "" {
			key = resolved
		}
	}
	lock, ok, err := w.Locker.Acquire(ctx, key, w.TTL)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOverlapping
	}
	defer func() { _ = lock.Release(ctx) }()
	return next(ctx, message)
}
