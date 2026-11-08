package temporalqueue

import (
	"errors"
	"go.temporal.io/sdk/temporal"
)

// ErrAdapterUnavailable marks a temporary key-service or adapter-service outage.
// Approved adapters return this classification only for availability failures.
// Conversion and propagation retry it three times per SDK callback; deterministic
// rejection and panics never enter that recovery loop.
var ErrAdapterUnavailable = errors.New("Temporal adapter temporarily unavailable")

// invalidAdapterError returns a sanitized terminal category even for attacker-selected workflow retry policies.
func invalidAdapterError() error {
	return &adapterRejection{cause: temporal.NewNonRetryableApplicationError("Temporal adapter rejected input", "goforj.invalid.v1", nil)}
}

// unavailableAdapterError preserves a safe retryable category after bounded local recovery.
func unavailableAdapterError() error {
	return temporal.NewApplicationError("Temporal adapter temporarily unavailable", "goforj.adapter-unavailable.v1")
}

// adapterUnavailable recognizes safe temporary classification across nested approved wrappers.
func adapterUnavailable(err error) bool {
	if errors.Is(err, ErrAdapterUnavailable) {
		return true
	}
	var application *temporal.ApplicationError
	return errors.As(err, &application) && application.Type() == "goforj.adapter-unavailable.v1"
}

// convertAdapter retries only explicitly temporary failures within a fixed invocation budget.
func convertAdapter[T any](call func() (T, error)) (result T, err error) {
	for range 3 {
		result, err = call()
		if err == nil {
			return result, nil
		}
		if !adapterUnavailable(err) {
			return result, invalidAdapterError()
		}
		if !errors.Is(err, ErrAdapterUnavailable) {
			return result, unavailableAdapterError()
		}
	}
	return result, unavailableAdapterError()
}

// adapterRejection identifies module-owned input rejection across SDK decode wrappers without changing native errors.
type adapterRejection struct{ cause error }

// Error keeps diagnostics fixed while retaining SDK error inspection through Unwrap.
func (e *adapterRejection) Error() string { return e.cause.Error() }

// Unwrap preserves errors.As for callers inspecting the safe SDK application category.
func (e *adapterRejection) Unwrap() error { return e.cause }

// terminalAdapterCause prevents SDK fmt wrappers from making deterministic module rejection retryable at persistence.
func terminalAdapterCause(err error) error {
	for cause, depth := err, 0; cause != nil && depth < 16; cause, depth = errors.Unwrap(cause), depth+1 {
		if rejection, ok := cause.(*adapterRejection); ok {
			return rejection.cause
		}
	}
	return err
}
