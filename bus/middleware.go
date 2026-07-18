package bus

import "github.com/goforj/queue"

// Next invokes the next workflow middleware or handler.
//
// Deprecated: use queue.Next.
type Next = queue.Next

// Middleware intercepts workflow job execution.
//
// Deprecated: use queue.Middleware.
type Middleware = queue.Middleware

// MiddlewareFunc adapts a function to workflow middleware.
//
// Deprecated: use queue.MiddlewareFunc.
type MiddlewareFunc = queue.MiddlewareFunc

// RetryPolicy is the legacy pass-through retry policy helper.
//
// Deprecated: use queue.RetryPolicy.
type RetryPolicy = queue.RetryPolicy

// SkipWhen skips execution when its predicate matches.
//
// Deprecated: use queue.SkipWhen.
type SkipWhen = queue.SkipWhen

// FailOnError converts matched errors into terminal failures.
//
// Deprecated: use queue.FailOnError.
type FailOnError = queue.FailOnError

// RateLimiter decides whether a keyed workflow job may execute.
//
// Deprecated: use queue.RateLimiter.
type RateLimiter = queue.RateLimiter

// RateLimit applies rate limiting before workflow job execution.
//
// Deprecated: use queue.RateLimit.
type RateLimit = queue.RateLimit

// Lock is released after overlap-protected execution completes.
//
// Deprecated: use queue.Lock.
type Lock = queue.Lock

// Locker acquires locks used to prevent overlapping execution.
//
// Deprecated: use queue.Locker.
type Locker = queue.Locker

// WithoutOverlapping prevents concurrent execution for one key.
//
// Deprecated: use queue.WithoutOverlapping.
type WithoutOverlapping = queue.WithoutOverlapping

var (
	// ErrSkipped indicates middleware intentionally skipped workflow job execution.
	//
	// Deprecated: use queue.ErrSkipped.
	ErrSkipped = queue.ErrSkipped
	// ErrRateLimited indicates middleware denied workflow job execution under its current rate limit.
	//
	// Deprecated: use queue.ErrRateLimited.
	ErrRateLimited = queue.ErrRateLimited
	// ErrOverlapping indicates middleware prevented overlapping workflow job execution.
	//
	// Deprecated: use queue.ErrOverlapping.
	ErrOverlapping = queue.ErrOverlapping
)
