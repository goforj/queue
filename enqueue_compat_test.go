package queue

import (
	"context"
	"time"
)

type enqueueOption func(Task) Task

func WithQueue(name string) enqueueOption {
	return func(t Task) Task { return t.OnQueue(name) }
}

func WithTimeout(timeout time.Duration) enqueueOption {
	return func(t Task) Task { return t.Timeout(timeout) }
}

func WithMaxRetry(maxRetry int) enqueueOption {
	return func(t Task) Task { return t.Retry(maxRetry) }
}

func WithBackoff(backoff time.Duration) enqueueOption {
	return func(t Task) Task { return t.Backoff(backoff) }
}

func WithDelay(delay time.Duration) enqueueOption {
	return func(t Task) Task { return t.Delay(delay) }
}

func WithUnique(ttl time.Duration) enqueueOption {
	return func(t Task) Task { return t.UniqueFor(ttl) }
}

func dispatch(q Queue, taskType string, payload []byte, opts ...enqueueOption) error {
	return enqueueCtx(q, context.Background(), taskType, payload, opts...)
}

func enqueueCtx(q Queue, ctx context.Context, taskType string, payload []byte, opts ...enqueueOption) error {
	task := NewTask(taskType).Payload(payload)
	for _, opt := range opts {
		task = opt(task)
	}
	if task.enqueueOptions().queueName == "" {
		task = task.OnQueue("default")
	}
	return q.Enqueue(ctx, task)
}
