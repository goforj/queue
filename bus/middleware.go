package bus

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Next func(ctx context.Context, jc Context) error

type Middleware interface {
	Handle(ctx context.Context, jc Context, next Next) error
}

type MiddlewareFunc func(ctx context.Context, jc Context, next Next) error

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

func (RetryPolicy) Handle(ctx context.Context, jc Context, next Next) error {
	return next(ctx, jc)
}

type SkipWhen struct {
	Predicate func(ctx context.Context, jc Context) bool
}

func (s SkipWhen) Handle(ctx context.Context, jc Context, next Next) error {
	if s.Predicate != nil && s.Predicate(ctx, jc) {
		return nil
	}
	return next(ctx, jc)
}

type FailOnError struct {
	When func(err error) bool
}

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
