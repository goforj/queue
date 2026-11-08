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
	return temporal.NewNonRetryableApplicationError("Temporal adapter rejected input", "goforj.invalid.v1", nil)
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
