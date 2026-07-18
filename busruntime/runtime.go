package busruntime

import (
	"context"
	"errors"
	"time"
)

// DeliveryAttempt identifies one application attempt and its configured retry budget.
// Number is zero-based, and MaxRetry is the number of retries after the initial attempt.
type DeliveryAttempt struct {
	Number   int
	MaxRetry int
}

// Exhausted reports whether the current application attempt has consumed its retry budget.
func (a DeliveryAttempt) Exhausted() bool {
	return a.Number >= a.MaxRetry
}

// AttemptDecision describes how a worker must settle an application attempt.
type AttemptDecision uint8

const (
	// AttemptSucceeded commits successful handler execution.
	AttemptSucceeded AttemptDecision = iota
	// AttemptRetry schedules a later application attempt after the current attempt failed.
	AttemptRetry
	// AttemptFailed commits a permanent or exhausted application failure.
	AttemptFailed
	// AttemptRedeliver retries infrastructure work without consuming the application retry budget.
	AttemptRedeliver
)

type deliveryAttemptContextKey struct{}

// WithDeliveryAttempt attaches physical delivery metadata for orchestration and middleware classification.
func WithDeliveryAttempt(ctx context.Context, attempt DeliveryAttempt) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deliveryAttemptContextKey{}, attempt)
}

// DeliveryAttemptFromContext returns physical delivery metadata when a worker supplied it.
func DeliveryAttemptFromContext(ctx context.Context) (DeliveryAttempt, bool) {
	if ctx == nil {
		return DeliveryAttempt{}, false
	}
	attempt, ok := ctx.Value(deliveryAttemptContextKey{}).(DeliveryAttempt)
	return attempt, ok
}

// ClassifyAttempt maps a handler result to the settlement decision owned by its worker or driver.
func ClassifyAttempt(attempt DeliveryAttempt, err error) AttemptDecision {
	switch {
	case err == nil:
		return AttemptSucceeded
	case IsUncommitted(err):
		return AttemptRedeliver
	case IsPermanent(err), attempt.Exhausted():
		return AttemptFailed
	default:
		return AttemptRetry
	}
}

type permanentError struct {
	cause error
}

// Error describes the permanent application failure.
func (e permanentError) Error() string {
	return e.cause.Error()
}

// Unwrap preserves errors.Is and errors.As behavior for the application failure.
func (e permanentError) Unwrap() error {
	return e.cause
}

// Permanent marks an application error as terminal regardless of remaining retries.
func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return permanentError{cause: err}
}

// IsPermanent reports whether an error requests terminal application settlement.
func IsPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}

type uncommittedError struct {
	cause error
}

// Error describes the infrastructure failure that prevented an outcome from being committed.
func (e uncommittedError) Error() string {
	return e.cause.Error()
}

// Unwrap preserves errors.Is and errors.As behavior for the infrastructure failure.
func (e uncommittedError) Unwrap() error {
	return e.cause
}

// Uncommitted marks an infrastructure error for redelivery without consuming application retries.
func Uncommitted(err error) error {
	if err == nil || IsUncommitted(err) {
		return err
	}
	return uncommittedError{cause: err}
}

// IsUncommitted reports whether an application outcome still needs to be committed.
func IsUncommitted(err error) bool {
	var target uncommittedError
	return errors.As(err, &target)
}

// InboundJob is the minimal job view the orchestration runtime needs from the queue layer.
type InboundJob interface {
	Bind(dst any) error
	PayloadBytes() []byte
}

// Handler processes one inbound delivery for the orchestration runtime.
type Handler func(ctx context.Context, job InboundJob) error

// JobOptions carries delivery policy from orchestration into the queue runtime.
type JobOptions struct {
	Queue     string
	Delay     time.Duration
	Timeout   time.Duration
	Retry     int
	Backoff   time.Duration
	UniqueFor time.Duration
}

// Runtime is the queue runtime surface required by the orchestration engine.
type Runtime interface {
	BusRegister(jobType string, handler Handler)
	BusDispatch(ctx context.Context, jobType string, payload []byte, opts JobOptions) error
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}
