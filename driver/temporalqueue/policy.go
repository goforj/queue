package temporalqueue

import (
	"errors"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"math"
	"time"
)

// activityPolicy persists resolved SDK policy separately from the original queue options.
type activityPolicy struct {
	Timeout         time.Duration
	Delay           time.Duration
	Backoff         time.Duration
	MaximumAttempts int32
}

// roundPolicySeconds implements the driver's whole-second policy without duration overflow.
func roundPolicySeconds(value time.Duration) (time.Duration, error) {
	if value < 0 {
		return 0, errors.New("Temporal duration must be nonnegative")
	}
	seconds := int64(value / time.Second)
	if value%time.Second != 0 {
		seconds++
	}
	if seconds > math.MaxInt64/int64(time.Second) {
		return 0, errors.New("Temporal duration rounding overflows")
	}
	return time.Duration(seconds) * time.Second, nil
}

// resolvePolicy freezes explicit queue options and the required resource timeout into SDK policy.
func resolvePolicy(timeout, delay, backoff time.Duration, retry int, defaultTimeout time.Duration) (activityPolicy, error) {
	if defaultTimeout < time.Second || timeout < 0 || delay < 0 || backoff < 0 || retry < 0 || int64(retry) > math.MaxInt32-1 {
		return activityPolicy{}, errors.New("invalid Temporal activity policy")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if backoff == 0 {
		backoff = time.Second
	}
	resolved := activityPolicy{MaximumAttempts: int32(retry + 1)}
	var err error
	resolved.Timeout, err = roundPolicySeconds(timeout)
	if err != nil {
		return activityPolicy{}, err
	}
	resolved.Delay, err = roundPolicySeconds(delay)
	if err != nil {
		return activityPolicy{}, err
	}
	resolved.Backoff, err = roundPolicySeconds(backoff)
	if err != nil {
		return activityPolicy{}, err
	}
	return resolved, nil
}

// resolve captures the resource timeout without erasing the original queue policy.
func (d *delivery) resolve(defaultTimeout time.Duration) error {
	policy, err := resolvePolicy(d.Timeout, d.Delay, d.Backoff, d.Retry, defaultTimeout)
	if err != nil {
		return err
	}
	d.DefaultTimeout = defaultTimeout
	d.Resolved = policy
	return nil
}

// awaitDeliveryDelay cancels only an unelapsed timer when journal authority is lost, never an accepted activity.
func awaitDeliveryDelay(ctx workflow.Context, delay time.Duration, broken func() bool) error {
	timerCtx, cancel := workflow.WithCancel(ctx)
	defer cancel()
	timer := workflow.NewTimer(timerCtx, delay)
	if err := workflow.Await(ctx, func() bool { return timer.IsReady() || broken() }); err != nil {
		return err
	}
	if broken() {
		return temporal.NewCanceledError()
	}
	return timer.Get(ctx, nil)
}
