package temporalqueue

import (
	"context"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
)

// payloadActivity matches all methods of the pinned SDK's payload-level activity interface.
// Execute-only structs follow SDK method registration and must not be mistaken for this interface.
type payloadActivity interface {
	Execute(context.Context, *common.Payloads) (*common.Payloads, error)
	ActivityType() activity.Type
	GetFunction() interface{}
}

// admittedPayloadActivity fences the SDK execution path that bypasses typed activity interceptors and converters.
type admittedPayloadActivity struct {
	inner    payloadActivity
	kind     activity.Type
	function any
	gate     *admission
	policy   *guardedConverter
}

// ActivityType returns the registration-time snapshot without reopening application callbacks during cleanup.
func (a *admittedPayloadActivity) ActivityType() activity.Type { return a.kind }

// GetFunction preserves SDK activity metadata using the registration-time function snapshot.
func (a *admittedPayloadActivity) GetFunction() interface{} { return a.function }

// Execute retains actual payload-level callback completion and applies the resource's encoded byte bounds.
func (a *admittedPayloadActivity) Execute(ctx context.Context, input *common.Payloads) (*common.Payloads, error) {
	release, err := a.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	if input != nil {
		if err := a.policy.check(input.Payloads); err != nil {
			return nil, err
		}
	}
	result, err := a.inner.Execute(ctx, input)
	if err != nil {
		return nil, err
	}
	if result != nil {
		if err := a.policy.check(result.Payloads); err != nil {
			return nil, err
		}
	}
	return result, nil
}
