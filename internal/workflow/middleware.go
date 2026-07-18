package workflow

import (
	"context"
)

// Next represents the remaining middleware and handler execution chain.
type Next func(ctx context.Context, jc Context) error

// Middleware can intercept logical workflow job execution.
type Middleware interface {
	// Handle wraps the remaining middleware and handler chain.
	Handle(ctx context.Context, jc Context, next Next) error
}

// chainMiddleware composes middleware in declaration order around the final handler.
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
