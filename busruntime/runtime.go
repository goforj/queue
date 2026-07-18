package busruntime

import (
	"context"
	"errors"
	"sync/atomic"
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
type continuationDispatchContextKey struct{}

type continuationScopeToken struct {
	identity byte
}

type continuationPermit struct {
	scope  *continuationScopeToken
	active atomic.Bool
}

// ContinuationScope owns short-lived permission for one runtime's handlers to enqueue descendants while that runtime drains.
// Its zero value grants no permission; use NewContinuationScope.
type ContinuationScope struct {
	token *continuationScopeToken
}

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

// NewContinuationScope creates an unforgeable runtime-specific continuation scope.
func NewContinuationScope() *ContinuationScope {
	return &ContinuationScope{token: &continuationScopeToken{}}
}

// Permit marks ctx only until the returned release function runs.
// Runtime adapters release the permit as the originating handler returns so escaped contexts cannot enqueue during a later drain.
func (s *ContinuationScope) Permit(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.token == nil {
		return ctx, func() {}
	}
	permit := &continuationPermit{scope: s.token}
	permit.active.Store(true)
	current := continuationPermits(ctx)
	permits := append(make([]*continuationPermit, 0, len(current)+1), current...)
	permits = append(permits, permit)
	marked := context.WithValue(ctx, continuationDispatchContextKey{}, permits)
	return marked, func() { permit.active.Store(false) }
}

// Owns reports whether ctx carries a still-active permit issued by this scope.
func (s *ContinuationScope) Owns(ctx context.Context) bool {
	if s == nil || s.token == nil {
		return false
	}
	for _, permit := range continuationPermits(ctx) {
		if permit != nil && permit.scope == s.token && permit.active.Load() {
			return true
		}
	}
	return false
}

// continuationPermits returns the immutable permit snapshot attached by nested runtime handlers.
func continuationPermits(ctx context.Context) []*continuationPermit {
	if ctx == nil {
		return nil
	}
	permits, _ := ctx.Value(continuationDispatchContextKey{}).([]*continuationPermit)
	return permits
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

// DeliveryMetadataVersion identifies the direct-delivery metadata understood
// by this version of the runtime and driver integration contract.
const DeliveryMetadataVersion = 1

// DeliveryMetadata carries correlation for an ordinary direct job without
// changing its application type or payload. Its JSON representation is the
// canonical persisted and transported driver metadata record.
type DeliveryMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	DispatchID    string `json:"dispatch_id,omitempty"`
	JobID         string `json:"job_id,omitempty"`
	ChainID       string `json:"chain_id,omitempty"`
	BatchID       string `json:"batch_id,omitempty"`
	Queue         string `json:"queue,omitempty"`
}

// Runtime is the queue runtime surface required by the orchestration engine.
type Runtime interface {
	BusRegister(jobType string, handler Handler)
	BusDispatch(ctx context.Context, jobType string, payload []byte, opts JobOptions) error
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// DirectRuntime extends Runtime with canonical direct-job dispatch. Its
// embedded Runtime registration method handles both application types and
// retained legacy envelopes.
type DirectRuntime interface {
	Runtime
	// BusDispatchDirect submits application bytes with correlation kept in the metadata channel.
	BusDispatchDirect(ctx context.Context, jobType string, payload []byte, metadata DeliveryMetadata, opts JobOptions) error
}

type deliveryMetadataContextKey struct{}

// WithDeliveryMetadata attaches direct-job correlation to one physical handler
// invocation without exposing transport framing to the application payload.
func WithDeliveryMetadata(ctx context.Context, metadata DeliveryMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deliveryMetadataContextKey{}, metadata)
}

// DeliveryMetadataFromContext returns supported direct-job correlation supplied
// by a compatible worker runtime. Missing and unknown versions are untrusted.
func DeliveryMetadataFromContext(ctx context.Context) (DeliveryMetadata, bool) {
	if ctx == nil {
		return DeliveryMetadata{}, false
	}
	metadata, ok := ctx.Value(deliveryMetadataContextKey{}).(DeliveryMetadata)
	if !ok || metadata.SchemaVersion != DeliveryMetadataVersion {
		return DeliveryMetadata{}, false
	}
	return metadata, true
}
