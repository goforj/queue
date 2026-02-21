package bus

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Next func(ctx context.Context, jc Context) error

// Middleware can intercept bus job execution.
// @group Middleware
type Middleware interface {
	Handle(ctx context.Context, jc Context, next Next) error
}

// MiddlewareFunc adapts a function to Middleware.
// @group Middleware
type MiddlewareFunc func(ctx context.Context, jc Context, next Next) error

// Handle calls the wrapped middleware function.
// @group Middleware
//
// Example: middleware func
//
//	mw := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
//		return next(ctx, jc)
//	})
//	_ = mw
func (f MiddlewareFunc) Handle(ctx context.Context, jc Context, next Next) error {
	return f(ctx, jc, next)
}

func chainMiddleware(middlewares []Middleware, final Next) Next {
	if len(middlewares) == 0 {
		return final
	}
	next := final
	for i := len(middlewares) - 1; i >= 0; i-- {
		m := middlewares[i]
		if m == nil {
			continue
		}
		currentNext := next
		next = func(ctx context.Context, jc Context) error {
			return m.Handle(ctx, jc, currentNext)
		}
	}
	return next
}

var (
	ErrSkipped     = errors.New("bus job skipped by middleware")
	ErrRateLimited = errors.New("bus job rate limited")
	ErrOverlapping = errors.New("bus job overlap prevented")
)

type RetryPolicy struct{}

// Handle passes execution through without modification.
// @group Middleware
//
// Example: retry policy passthrough
//
//	policy := bus.RetryPolicy{}
//	_ = policy
func (RetryPolicy) Handle(ctx context.Context, jc Context, next Next) error {
	return next(ctx, jc)
}

type SkipWhen struct {
	Predicate func(ctx context.Context, jc Context) bool
}

// Handle skips job execution when Predicate returns true.
// @group Middleware
//
// Example: skip by predicate
//
//	mw := bus.SkipWhen{
//		Predicate: func(context.Context, bus.Context) bool { return true },
//	}
//	_ = mw
func (s SkipWhen) Handle(ctx context.Context, jc Context, next Next) error {
	if s.Predicate != nil && s.Predicate(ctx, jc) {
		return nil
	}
	return next(ctx, jc)
}

type FailOnError struct {
	When func(err error) bool
}

// Handle wraps matched errors as fatal errors to stop retries.
// @group Middleware
//
// Example: fail on any error
//
//	mw := bus.FailOnError{
//		When: func(err error) bool { return err != nil },
//	}
//	_ = mw
func (f FailOnError) Handle(ctx context.Context, jc Context, next Next) error {
	err := next(ctx, jc)
	if err == nil {
		return nil
	}
	if f.When == nil || f.When(err) {
		return fatalError{cause: err}
	}
	return err
}

type fatalError struct {
	cause error
}

func (f fatalError) Error() string { return fmt.Sprintf("fatal bus error: %v", f.cause) }
func (f fatalError) Unwrap() error { return f.cause }

type RateLimiter interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

type RateLimit struct {
	Key     func(ctx context.Context, jc Context) string
	Limiter RateLimiter
}

// Handle applies limiter checks before executing the next handler.
// @group Middleware
//
// Example: rate limit middleware
//
//	mw := bus.RateLimit{
//		Key: func(context.Context, bus.Context) string { return "emails" },
//	}
//	_ = mw
func (r RateLimit) Handle(ctx context.Context, jc Context, next Next) error {
	if r.Limiter == nil {
		return next(ctx, jc)
	}
	key := jc.JobType
	if r.Key != nil {
		if k := r.Key(ctx, jc); k != "" {
			key = k
		}
	}
	allowed, _, err := r.Limiter.Allow(ctx, key)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrRateLimited
	}
	return next(ctx, jc)
}

type Lock interface {
	Release(ctx context.Context) error
}

type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, bool, error)
}

type WithoutOverlapping struct {
	Key    func(ctx context.Context, jc Context) string
	TTL    time.Duration
	Locker Locker
}

// Handle acquires a lock and prevents concurrent overlap for the same key.
// @group Middleware
//
// Example: without overlapping
//
//	mw := bus.WithoutOverlapping{
//		Key: func(context.Context, bus.Context) string { return "job-key" },
//		TTL: 30 * time.Second,
//	}
//	_ = mw
func (w WithoutOverlapping) Handle(ctx context.Context, jc Context, next Next) error {
	if w.Locker == nil {
		return next(ctx, jc)
	}
	key := jc.JobType
	if w.Key != nil {
		if k := w.Key(ctx, jc); k != "" {
			key = k
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
	return next(ctx, jc)
}
