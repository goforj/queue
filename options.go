package queue

import (
	"time"
)

// Option mutates enqueue behavior.
// @group Options
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

// WithQueue routes a task to a named queue.
// @group Options
//
// Example: with queue
//
//	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
//	ctx := context.Background()
//	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithQueue("critical"))
//	_ = err
func WithQueue(name string) Option {
	return optionFunc(func(target *enqueueOptions) {
		target.queueName = name
	})
}

// WithTimeout sets per-task execution timeout.
// @group Options
//
// Example: with timeout
//
//	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
//	ctx := context.Background()
//	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithTimeout(15*time.Second))
//	_ = err
func WithTimeout(timeout time.Duration) Option {
	return optionFunc(func(target *enqueueOptions) {
		target.timeout = &timeout
	})
}

// WithMaxRetry sets maximum retry attempts.
// @group Options
//
// Example: with max retry
//
//	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
//	ctx := context.Background()
//	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithMaxRetry(3))
//	_ = err
func WithMaxRetry(maxRetry int) Option {
	return optionFunc(func(target *enqueueOptions) {
		target.maxRetry = &maxRetry
	})
}

// WithBackoff sets delay between retry attempts.
// @group Options
//
// Example: with backoff
//
//	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
//	ctx := context.Background()
//	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithBackoff(2*time.Second))
//	_ = err
func WithBackoff(backoff time.Duration) Option {
	return optionFunc(func(target *enqueueOptions) {
		target.backoff = &backoff
	})
}

// WithDelay schedules processing after a delay.
// @group Options
//
// Example: with delay
//
//	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
//	ctx := context.Background()
//	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithDelay(10*time.Second))
//	_ = err
func WithDelay(delay time.Duration) Option {
	return optionFunc(func(target *enqueueOptions) {
		target.delay = delay
	})
}

// WithUnique deduplicates by task type and payload for a TTL window.
// @group Options
//
// Example: with unique
//
//	dispatcher, err := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
//	ctx := context.Background()
//	err = dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send", Payload: []byte(`{"id":1}`)}, queue.WithUnique(30*time.Second))
//	_ = err
func WithUnique(ttl time.Duration) Option {
	return optionFunc(func(target *enqueueOptions) {
		target.uniqueTTL = ttl
	})
}
