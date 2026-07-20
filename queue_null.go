package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/queue/internal/uniqueness"
)

type nullQueue struct {
	unique uniqueness.MemoryStore
}

func newNullQueue() queueBackend {
	return &nullQueue{}
}

func (q *nullQueue) Driver() Driver {
	return DriverNull
}

func (q *nullQueue) Register(string, Handler) {}

func (q *nullQueue) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (q *nullQueue) Dispatch(ctx context.Context, job Job) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := job.validate(); err != nil {
		return err
	}
	opts := job.jobOptions()
	if opts.queueName == "" {
		return fmt.Errorf("job queue is required")
	}
	if opts.uniqueTTL > 0 {
		if !q.claimUnique(job, opts.queueName, opts.uniqueTTL) {
			return ErrDuplicate
		}
	}
	return nil
}

func (q *nullQueue) Shutdown(context.Context) error {
	return nil
}

// DrainWorkers completes immediately because the null backend executes no work.
func (q *nullQueue) DrainWorkers(context.Context) error {
	return nil
}

func (q *nullQueue) Ready(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// claimUnique records the null backend's accepted TTL window.
func (q *nullQueue) claimUnique(job Job, queueName string, ttl time.Duration) bool {
	_, ok := q.unique.Acquire(DriverUniqueKey(job, queueName), ttl)
	return ok
}
