package queue

import "time"

// Option mutates enqueue behavior.
type Option interface {
    apply(*enqueueOptions)
}

type optionFunc func(*enqueueOptions)

func (f optionFunc) apply(target *enqueueOptions) {
    f(target)
}

type enqueueOptions struct {
    queueName string
    timeout   *time.Duration
    maxRetry  *int
    backoff   *time.Duration
    delay     time.Duration
    uniqueTTL time.Duration
}

func resolveOptions(opts ...Option) enqueueOptions {
    var out enqueueOptions
    for i := range opts {
        if opts[i] == nil {
            continue
        }
        opts[i].apply(&out)
    }
    return out
}

// WithQueue sets the queue name.
func WithQueue(name string) Option {
    return optionFunc(func(target *enqueueOptions) {
        target.queueName = name
    })
}

// WithTimeout sets task timeout.
func WithTimeout(timeout time.Duration) Option {
    return optionFunc(func(target *enqueueOptions) {
        target.timeout = &timeout
    })
}

// WithMaxRetry sets max retry attempts.
func WithMaxRetry(maxRetry int) Option {
    return optionFunc(func(target *enqueueOptions) {
        target.maxRetry = &maxRetry
    })
}

// WithBackoff sets delay between retry attempts.
func WithBackoff(backoff time.Duration) Option {
    return optionFunc(func(target *enqueueOptions) {
        target.backoff = &backoff
    })
}

// WithDelay schedules processing after a delay.
func WithDelay(delay time.Duration) Option {
    return optionFunc(func(target *enqueueOptions) {
        target.delay = delay
    })
}

// WithUnique deduplicates by type+payload for ttl.
func WithUnique(ttl time.Duration) Option {
    return optionFunc(func(target *enqueueOptions) {
        target.uniqueTTL = ttl
    })
}
