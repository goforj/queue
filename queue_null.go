package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type nullQueue struct {
	mu     sync.Mutex
	unique map[string]time.Time
}

func newNullQueue() queueBackend {
	return &nullQueue{unique: make(map[string]time.Time)}
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

func (q *nullQueue) Dispatch(ctx context.Context, task Job) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := task.validate(); err != nil {
		return err
	}
	opts := task.jobOptions()
	if opts.queueName == "" {
		return fmt.Errorf("job queue is required")
	}
	if opts.uniqueTTL > 0 {
		if !q.claimUnique(task, opts.queueName, opts.uniqueTTL) {
			return ErrDuplicate
		}
	}
	return nil
}

func (q *nullQueue) Shutdown(context.Context) error {
	return nil
}

func (q *nullQueue) claimUnique(task Job, queueName string, ttl time.Duration) bool {
	now := time.Now()
	key := queueName + ":" + taskEventKey(task)
	q.mu.Lock()
	defer q.mu.Unlock()
	for k, expiresAt := range q.unique {
		if now.After(expiresAt) {
			delete(q.unique, k)
		}
	}
	if expiresAt, ok := q.unique[key]; ok && now.Before(expiresAt) {
		return false
	}
	q.unique[key] = now.Add(ttl)
	return true
}
